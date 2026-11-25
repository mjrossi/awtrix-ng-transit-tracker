package awtrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
)

const originPushed = "pushed"

// builtinNames are the built-in apps of all clock models together, as of
// firmware commit d51e266. A pushed app of the same name hides a built-in one
// from the app list, and there is no lookup for them as there is for scripts.
var builtinNames = []string{"Time", "Status", "Date", "Temperature", "Humidity", "Battery"}

// listedApp is one entry of GET /api/v1/apps.
type listedApp struct {
	Name string `json:"name"`
	// Enabled is nil for script modules, which are not in the rotation.
	Enabled *bool `json:"enabled"`
	Slot    *int  `json:"slot"`
	// Origin is empty for a name that only keeps its place.
	Origin  string `json:"origin"`
	Present bool   `json:"present"`
}

// placeholder reports whether the entry only keeps a place in the
// rotation, such as one left by an expired app.
func (a listedApp) placeholder() bool { return a.Origin == "" && !a.Present }

// ForgetApps removes pushed apps and the places they keep in the clock's
// rotation, which outlive the apps, and returns the names it found and
// removed. A name that also belongs to a script or built-in app loses its
// pushed app but keeps its place, and is not returned. Any doubtful answer
// from the clock is an error, before anything is written.
func (c *Client) ForgetApps(ctx context.Context, names []string) ([]string, error) {
	scripts := map[string]bool{}
	for _, n := range names {
		if !ValidAppName(n) {
			continue
		}
		has, err := c.hasScript(ctx, n)
		if err != nil {
			return nil, err
		}
		scripts[n] = has
	}
	if len(scripts) == 0 {
		return nil, nil
	}
	scripting, err := c.scriptingRunning(ctx)
	if err != nil {
		return nil, err
	}
	// Ownership and the new order come from the same list, read just before
	// the write, to leave little time for a change in the web UI to be lost.
	apps, err := c.listApps(ctx)
	if err != nil {
		return nil, err
	}
	var forget, removed, expire []string
	for n, owned := range scripts {
		owned = owned || slices.Contains(builtinNames, n)
		known := false
		for _, a := range apps {
			if a.Name != n {
				continue
			}
			known = true
			switch {
			case a.Origin == originPushed:
				expire = append(expire, n)
			case !a.placeholder():
				owned = true
			}
		}
		if owned {
			continue
		}
		forget = append(forget, n)
		if known {
			removed = append(removed, n)
		}
	}
	// The order is written even if it does not change: after a 507 the clock
	// uses it without having saved it, until it is written again.
	if len(forget) > 0 {
		if err := c.putOrder(ctx, apps, forget, scripting); err != nil {
			return nil, err
		}
	}
	slices.Sort(expire)
	for _, n := range slices.Compact(expire) {
		if err := c.ExpireApp(ctx, n); err != nil {
			return nil, err
		}
	}
	slices.Sort(removed)
	return removed, nil
}

// hasScript reports whether the clock stores a script called name. Only the
// firmware's own error codes count as no: a proxy or a busy clock can answer
// with the same statuses.
func (c *Client) hasScript(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/api/v1/apps/script/"+name, nil, nil)
	if e, ok := errors.AsType[*APIError](err); ok &&
		(e.Status == http.StatusNotFound && e.Code == "notFound" ||
			e.Status == http.StatusServiceUnavailable && e.Code == "unavailable") {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) scriptingRunning(ctx context.Context) (bool, error) {
	var device struct {
		ScriptingRunning *bool `json:"scriptingRunning"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/device", nil, &device); err != nil {
		return false, err
	}
	if device.ScriptingRunning == nil {
		return false, errors.New("awtrix: GET /api/v1/device: no scriptingRunning")
	}
	return *device.ScriptingRunning, nil
}

// putOrder writes the arrangement in apps without the names in forget. The
// clock only accepts the whole arrangement.
func (c *Client) putOrder(ctx context.Context, apps []listedApp, forget []string, scripting bool) error {
	body := struct {
		Order    []string `json:"order"`
		Disabled []string `json:"disabled"`
	}{Order: []string{}, Disabled: []string{}}
	for _, a := range apps {
		if a.Enabled == nil || slices.Contains(forget, a.Name) {
			continue
		}
		// With scripting off, the clock lists every stored script again
		// without a slot and as disabled, whatever its saved switch.
		if a.Origin == "script" && !scripting && a.Slot == nil {
			continue
		}
		if a.Slot != nil {
			body.Order = append(body.Order, a.Name)
		}
		if !*a.Enabled && !slices.Contains(body.Disabled, a.Name) {
			body.Disabled = append(body.Disabled, a.Name)
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("awtrix: encode order: %w", err)
	}
	return c.do(ctx, http.MethodPut, "/api/v1/apps/order", b, nil)
}

// listApps returns the clock's apps, those in the rotation first and in
// order. An app shown twice per round is listed twice.
func (c *Client) listApps(ctx context.Context) ([]listedApp, error) {
	var apps []listedApp
	if err := c.do(ctx, http.MethodGet, "/api/v1/apps", nil, &apps); err != nil {
		return nil, err
	}
	if err := checkApps(apps); err != nil {
		return nil, fmt.Errorf("awtrix: GET /api/v1/apps: %w", err)
	}
	return apps, nil
}

// checkApps rejects a list that cannot be the clock's complete one, because
// a rotation rebuilt from it would lose apps.
func checkApps(apps []listedApp) error {
	slots, rest, builtin := 0, false, false
	for _, a := range apps {
		switch {
		case a.Name == "":
			return errors.New("app without a name")
		case a.Enabled == nil && a.Origin != "module":
			return fmt.Errorf("app %q without enabled", a.Name)
		case a.Slot == nil:
			rest = true
		case rest || *a.Slot != slots:
			return fmt.Errorf("app %q in unexpected slot %d", a.Name, *a.Slot)
		default:
			slots++
		}
		builtin = builtin || a.Origin == "builtin"
	}
	if !builtin {
		return errors.New("no built-in app listed")
	}
	return nil
}

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

// pushed reports whether the entry is a pushed app.
func (a listedApp) pushed() bool { return a.Origin == originPushed }

// placeholder reports whether the entry only keeps a place in the
// rotation, left behind by an app that is gone or by a script refused at
// boot. A placeholder must not be deleted: DELETE on its name would
// remove a script's source too. It only needs the order rewrite.
func (a listedApp) placeholder() bool { return a.Origin == "" && !a.Present }

// ForgetApps removes pushed apps and the places they keep in the clock's
// rotation, which outlive both an app's lifetime and DeleteApp, and
// returns the names it actually removed. Names the clock does not know,
// or that belong to a script or built-in app, are left alone: when a
// pushed app and a script share a name, the clock lists only the pushed
// one, and DELETE on the name would remove the script's source too, so
// the script API is asked first.
func (c *Client) ForgetApps(ctx context.Context, names []string) ([]string, error) {
	apps, err := c.listApps(ctx)
	if err != nil {
		return nil, err
	}
	var deletes, places []string
	for _, n := range names {
		isEntry := func(a listedApp) bool { return a.Name == n }
		if !slices.ContainsFunc(apps, isEntry) {
			continue
		}
		// A script, built-in or module listed under the same name: leave
		// the whole name alone.
		if slices.ContainsFunc(apps, func(a listedApp) bool {
			return isEntry(a) && !a.pushed() && !a.placeholder()
		}) {
			continue
		}
		if slices.ContainsFunc(apps, func(a listedApp) bool { return isEntry(a) && a.pushed() }) {
			has, err := c.hasScript(ctx, n)
			if err != nil {
				return nil, err
			}
			if has {
				continue
			}
			deletes = append(deletes, n)
		} else {
			// Only a placeholder: no DELETE, just the order rewrite.
			places = append(places, n)
		}
	}
	for _, n := range deletes {
		if err := c.DeleteApp(ctx, n); err != nil {
			return nil, err
		}
	}
	removed := slices.Concat(deletes, places)
	if err := c.forgetPlaces(ctx, removed); err != nil {
		return nil, err
	}
	return removed, nil
}

// hasScript reports whether the clock stores a script called name. DELETE
// on that name removes the script's source as well, so callers check
// first. A clock without scripting answers 503, which also means no
// scripts exist.
func (c *Client) hasScript(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/api/v1/apps/script/"+name, nil, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusNotFound, http.StatusServiceUnavailable:
			return false, nil
		}
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// forgetPlaces drops names from the rotation. The clock only accepts the
// whole arrangement, so it is read again just before it is written, to leave
// little time for a change in the web UI to be lost.
func (c *Client) forgetPlaces(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	var device struct {
		ScriptingRunning *bool `json:"scriptingRunning"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/device", nil, &device); err != nil {
		return err
	}
	if device.ScriptingRunning == nil {
		return errors.New("awtrix: GET /api/v1/device: no scriptingRunning")
	}
	scripting := *device.ScriptingRunning
	apps, err := c.listApps(ctx)
	if err != nil {
		return err
	}
	body := struct {
		Order    []string `json:"order"`
		Disabled []string `json:"disabled"`
	}{Order: []string{}, Disabled: []string{}}
	changed := false
	for _, a := range apps {
		// Script modules are not in the rotation. With scripting off, the
		// clock lists every stored script as disabled; only a script
		// without a slot is left out of the rewrite, so a slotted one
		// keeps its place in the rotation.
		if a.Enabled == nil || (a.Origin == "script" && !scripting && a.Slot == nil) {
			continue
		}
		if slices.Contains(names, a.Name) {
			changed = changed || a.Slot != nil || !*a.Enabled
			continue
		}
		if a.Slot != nil {
			body.Order = append(body.Order, a.Name)
		}
		if !*a.Enabled && !slices.Contains(body.Disabled, a.Name) {
			body.Disabled = append(body.Disabled, a.Name)
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("awtrix: encode order: %w", err)
	}
	return c.do(ctx, http.MethodPut, "/api/v1/apps/order", b, nil)
}

// listApps returns the clock's apps, those in the rotation first and in
// order. An app shown twice per round is listed twice. An empty list is an
// error: the clock always lists its built-in apps, so null or [] means the
// answer was not a real app list, and acting on it could wipe the rotation.
func (c *Client) listApps(ctx context.Context) ([]listedApp, error) {
	var apps []listedApp
	if err := c.do(ctx, http.MethodGet, "/api/v1/apps", nil, &apps); err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, errors.New("awtrix: GET /api/v1/apps: empty app list")
	}
	return apps, nil
}

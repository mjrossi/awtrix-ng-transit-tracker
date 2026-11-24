package awtrix

import (
	"context"
	"encoding/json"
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

// forgettable reports whether the entry is a pushed app, or the place one
// left behind. Deleting a script's name would delete its source.
func (a listedApp) forgettable() bool {
	return a.Origin == originPushed || (a.Origin == "" && !a.Present)
}

// ForgetApps removes pushed apps and the places they keep in the clock's
// rotation, which outlive both an app's lifetime and DeleteApp. Names the
// clock does not know, or that belong to a script or built-in app, are left
// alone.
func (c *Client) ForgetApps(ctx context.Context, names []string) error {
	apps, err := c.listApps(ctx)
	if err != nil {
		return err
	}
	var forget []string
	for _, n := range names {
		isEntry := func(a listedApp) bool { return a.Name == n }
		if slices.ContainsFunc(apps, isEntry) &&
			!slices.ContainsFunc(apps, func(a listedApp) bool { return isEntry(a) && !a.forgettable() }) {
			forget = append(forget, n)
		}
	}
	for _, n := range forget {
		if err := c.DeleteApp(ctx, n); err != nil {
			return err
		}
	}
	return c.forgetPlaces(ctx, forget)
}

// forgetPlaces drops names from the rotation. The clock only accepts the
// whole arrangement, so it is read again just before it is written, to leave
// little time for a change in the web UI to be lost.
func (c *Client) forgetPlaces(ctx context.Context, names []string) error {
	if len(names) == 0 {
		return nil
	}
	var device struct {
		ScriptingRunning bool `json:"scriptingRunning"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/device", nil, &device); err != nil {
		return err
	}
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
		// With scripting off, the clock lists every stored script as
		// disabled; the arrangement itself lists them without an origin.
		if a.Enabled == nil || (a.Origin == "script" && !device.ScriptingRunning) {
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
// order. An app shown twice per round is listed twice.
func (c *Client) listApps(ctx context.Context) ([]listedApp, error) {
	var apps []listedApp
	err := c.do(ctx, http.MethodGet, "/api/v1/apps", nil, &apps)
	return apps, err
}

package awtrix_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
)

// PushApp sends an app to the clock. Setting LifetimeMs makes the clock drop
// it if the sender stops.
func ExampleClient_PushApp() {
	clock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println(r.Method, r.URL.Path)
	}))
	defer clock.Close()

	c, err := awtrix.NewClient(clock.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	app := awtrix.App{Text: awtrix.PlainText("Hello"), LifetimeMs: 180_000}
	if err := c.PushApp(context.Background(), "hello", app); err != nil {
		fmt.Println(err)
	}
	// Output: PUT /api/v1/apps/pushed/hello
}

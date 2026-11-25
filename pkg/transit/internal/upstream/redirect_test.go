package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// redirecting answers requests for each URL in to with a redirect, records
// the last request, and answers any other with 200.
func redirecting(to map[string]string, last **http.Request) *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		*last = r
		w := httptest.NewRecorder()
		if loc, ok := to[r.URL.String()]; ok {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusFound)
		}
		resp := w.Result()
		resp.Request = r
		return resp, nil
	})}
}

func get(t *testing.T, c *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Api-Key", "secret")
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

func TestKeepHeadersOnHost(t *testing.T) {
	tests := map[string]struct {
		hops     []string
		wantKept bool
	}{
		"same host":          {[]string{"https://a.example/1?key=k", "https://a.example/2"}, true},
		"upgrade to https":   {[]string{"http://a.example/1?key=k", "https://a.example/1"}, true},
		"downgrade to http":  {[]string{"https://a.example/1?key=k", "http://a.example/1"}, false},
		"another host":       {[]string{"https://a.example/1?key=k", "https://b.example/1"}, false},
		"a subdomain":        {[]string{"https://a.example/1?key=k", "https://x.a.example/1"}, false},
		"another port":       {[]string{"https://a.example/1?key=k", "https://a.example:8443/1"}, false},
		"further on the way": {[]string{"https://a.example/1?key=k", "https://b.example/1?key=k", "https://b.example/2"}, false},
		"there and back":     {[]string{"https://a.example/1?key=k", "https://b.example/1", "https://a.example/2"}, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			to := map[string]string{}
			for i, u := range tt.hops[1:] {
				to[tt.hops[i]] = u
			}
			var last *http.Request
			client := redirecting(to, &last)
			if err := get(t, KeepHeadersOnHost(client, "x-api-key"), tt.hops[0]); err != nil {
				t.Fatal(err)
			}
			if got, want := last.URL.String(), tt.hops[len(tt.hops)-1]; got != want {
				t.Fatalf("ended at %s, want %s", got, want)
			}
			if got := last.Header.Get("X-Api-Key") != ""; got != tt.wantKept {
				t.Errorf("key sent = %v, want %v", got, tt.wantKept)
			}
			// The address redirected from can hold a key in its query.
			if ref := last.Header.Get("Referer"); !tt.wantKept && ref != "" {
				t.Errorf("Referer %q sent to another host", ref)
			}
			if client.CheckRedirect != nil {
				t.Error("the caller's client was modified")
			}
		})
	}
}

func TestKeepHeadersOnHostStopsRedirecting(t *testing.T) {
	loop := map[string]string{"https://a.example/": "https://a.example/"}
	refused := errors.New("refused")
	var last *http.Request
	if err := get(t, KeepHeadersOnHost(redirecting(loop, &last)), "https://a.example/"); err == nil {
		t.Error("followed an endless redirect")
	}
	client := redirecting(loop, &last)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return refused }
	if err := get(t, KeepHeadersOnHost(client), "https://a.example/"); !errors.Is(err, refused) {
		t.Errorf("err = %v, want the client's own check to apply", err)
	}
}

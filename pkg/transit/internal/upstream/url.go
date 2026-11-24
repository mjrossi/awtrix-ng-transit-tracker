package upstream

import "net/url"

// HTTPURL parses raw and reports whether it is an absolute http(s) URL.
func HTTPURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	return u, err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

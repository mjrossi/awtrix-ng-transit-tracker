package upstream

import (
	"fmt"
	"net/url"
)

// HTTPURL parses raw, the value of option, as an absolute http(s) URL. Its
// error names the option but never quotes raw, which can hold credentials.
func HTTPURL(option, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s must be an http(s) URL", option)
	}
	return u, nil
}

package upstream

import (
	"errors"
	"net/http"
	"net/url"
)

// maxRedirects is net/http's own limit, which a CheckRedirect replaces.
const maxRedirects = 10

// KeepHeadersOnHost returns a copy of client that sends the named headers,
// such as one holding an API key, only to the host first asked, and never over
// http after https. net/http drops only Authorization and cookies, and not for
// a subdomain or a change of scheme. The redirect is still followed, without
// Referer, whose URL can hold a key too.
func KeepHeadersOnHost(client *http.Client, headers ...string) *http.Client {
	c := *client
	check := client.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameHost(via[0].URL, req.URL) {
			req.Header.Del("Referer")
			for _, h := range headers {
				req.Header.Del(h)
			}
		}
		if check != nil {
			return check(req, via)
		}
		if len(via) >= maxRedirects {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}

// sameHost reports whether to is from's host and no less secure.
func sameHost(from, to *url.URL) bool {
	return to.Host == from.Host && (to.Scheme == from.Scheme || to.Scheme == "https")
}

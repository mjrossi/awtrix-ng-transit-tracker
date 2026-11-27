package gtfsrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
)

func (p *Provider) fetchFeed(ctx context.Context, u string) (feed, error) {
	b, err := p.fetch(ctx, u, maxFeedBody)
	if err != nil {
		return feed{}, err
	}
	f, err := decodeFeed(b)
	if err != nil {
		return feed{}, fmt.Errorf("%s: %s: %w", Name, redact(u), err)
	}
	return f, nil
}

func (p *Provider) fetch(ctx context.Context, u string, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := p.fetchTo(ctx, u, nil, limit, to(&buf)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// to sends every 200 response's body to w.
func to(w io.Writer) func(int, http.Header) (io.Writer, error) {
	return func(int, http.Header) (io.Writer, error) { return w, nil }
}

// fetchTo copies the body of a GET, sent with the extra headers, to the
// writer that dest picks from the response's status and headers, failing
// above limit bytes. dest sees 200 responses, and 304 ones when extra makes
// the request conditional. Errors name the URL without its query string, which may hold
// an API key.
func (p *Provider) fetchTo(ctx context.Context, u string, extra http.Header, limit int64, dest func(status int, h http.Header) (io.Writer, error)) (int64, error) {
	shown := redact(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("%s: GET %s: invalid request", Name, shown)
	}
	req.Header.Set("User-Agent", upstream.UserAgent)
	for k, v := range p.cfg.Headers {
		req.Header.Set(k, v)
	}
	for k := range extra {
		req.Header.Set(k, extra.Get(k))
	}
	resp, err := p.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	defer resp.Body.Close()
	conditional := len(extra) > 0 && resp.StatusCode == http.StatusNotModified
	if resp.StatusCode != http.StatusOK && !conditional {
		upstream.Drain(resp.Body)
		return 0, fmt.Errorf("%s: GET %s: %s", Name, shown, resp.Status)
	}
	w, err := dest(resp.StatusCode, resp.Header)
	if err != nil {
		upstream.Drain(resp.Body)
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, fmt.Errorf("%s: GET %s: %w", Name, shown, err)
	}
	if n > limit {
		return 0, fmt.Errorf("%s: GET %s: response over %d MB", Name, shown, limit>>20)
	}
	return n, nil
}

// redact drops the credentials and query string from u for messages.
func redact(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return "(invalid URL)"
	}
	p.User = nil
	if p.RawQuery != "" {
		p.RawQuery = "..."
	}
	p.Fragment = ""
	return p.String()
}

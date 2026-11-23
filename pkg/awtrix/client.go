package awtrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// appName is the firmware's rule for app names; others get 400 invalidName.
var appName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ValidAppName reports whether name is accepted by AWTRIX NG.
func ValidAppName(name string) bool { return appName.MatchString(name) }

// Client talks to one clock over HTTP.
type Client struct {
	base     *url.URL
	http     *http.Client
	user     string
	password string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithBasicAuth sets credentials for clocks with authEnabled.
func WithBasicAuth(user, password string) Option {
	return func(c *Client) { c.user, c.password = user, password }
}

// NewClient returns a client for the clock at baseURL, such as
// "http://awtrixng-a1b2c3.local". A bare host gets "http://".
func NewClient(baseURL string, opts ...Option) (*Client, error) {
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		// url.Error quotes the raw URL, which can embed a password.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("clock url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("clock url: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("clock url: missing host")
	}
	c := &Client{base: u, http: &http.Client{Timeout: 10 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// APIError is an error answer from the clock.
type APIError struct {
	Status  int
	Code    string
	Message string
	Field   string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "awtrix: %d", e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Field != "" {
		fmt.Fprintf(&b, " (field %s)", e.Field)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	return b.String()
}

// PushApp creates or replaces the pushed app called name.
func (c *Client) PushApp(ctx context.Context, name string, app App) error {
	if !ValidAppName(name) {
		return fmt.Errorf("awtrix: invalid app name %q", name)
	}
	body, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("awtrix: encode app: %w", err)
	}
	return c.do(ctx, http.MethodPut, "/api/v1/apps/pushed/"+name, body)
}

// DeleteApp removes the app called name. An app that is already gone,
// such as after its lifetime expired or the clock restarted, counts as
// removed.
func (c *Client) DeleteApp(ctx context.Context, name string) error {
	if !ValidAppName(name) {
		return fmt.Errorf("awtrix: invalid app name %q", name)
	}
	err := c.do(ctx, http.MethodDelete, "/api/v1/apps/"+name, nil)
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) error {
	u := c.base.JoinPath(path)
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return fmt.Errorf("awtrix: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("awtrix: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return decodeError(resp)
}

func decodeError(resp *http.Response) error {
	e := &APIError{Status: resp.StatusCode}
	var wire struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Field   string `json:"field"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(b, &wire) == nil {
		e.Code, e.Message, e.Field = wire.Error.Code, wire.Error.Message, wire.Error.Field
	}
	if e.Code == "" && e.Message == "" {
		e.Message = strings.TrimSpace(string(b))
	}
	return e
}

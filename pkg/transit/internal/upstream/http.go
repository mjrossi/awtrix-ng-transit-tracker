package upstream

import (
	"io"
	"net/http"
	"time"
)

// UserAgent identifies the service in every upstream request.
const UserAgent = "urbanist-timetable"

const (
	clientTimeout = 20 * time.Second
	// maxDrain is more than an error page holds. Past it, a new connection
	// costs less than reading on.
	maxDrain = 256 << 10
)

// ClientOrDefault returns client, or one with a timeout if it is nil.
func ClientOrDefault(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: clientTimeout}
	}
	return client
}

// Drain reads what is left of an unwanted body, up to a limit, so that its
// connection can be reused.
func Drain(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrain))
}

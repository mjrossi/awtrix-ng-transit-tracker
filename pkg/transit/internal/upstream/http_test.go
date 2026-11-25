package upstream

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientOrDefault(t *testing.T) {
	if got := ClientOrDefault(nil).Timeout; got != 20*time.Second {
		t.Errorf("default timeout = %s, want 20s", got)
	}
	own := &http.Client{}
	if ClientOrDefault(own) != own {
		t.Error("the caller's client was replaced")
	}
}

func TestDrainIsBounded(t *testing.T) {
	body := strings.NewReader(strings.Repeat("x", 2*maxDrain))
	Drain(body)
	if got := body.Len(); got != maxDrain {
		t.Errorf("left %d bytes, want %d", got, maxDrain)
	}
}

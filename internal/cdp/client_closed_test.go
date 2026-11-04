package cdp

import (
	"errors"
	"strings"
	"testing"
)

// A browser the remote side stopped leaves the client in place: Closed is what
// `browser status` reads to say the session is gone, and why.
func TestClosedIsNilWhileOpenThenNamesTheCause(t *testing.T) {
	c := &Client{doneCh: make(chan struct{})}

	if err := c.Closed(); err != nil {
		t.Fatalf("an open connection reported closed: %v", err)
	}

	c.err = errors.New("failed to get reader: failed to read frame header: EOF")
	close(c.doneCh)

	err := c.Closed()
	if !errors.Is(err, errConnClosed) {
		t.Fatalf("Closed() = %v, want errConnClosed", err)
	}
	if !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("Closed() = %v, want the cause kept", err)
	}
}

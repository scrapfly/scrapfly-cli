package cdp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A failed dial quotes the URL it requested; no credential of it reaches the
// error a command prints.
func TestDialErrorHoldsNoCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Dial(ctx, "ws://127.0.0.1:1/?api_key=scp-live-dialsecret&vault_key=vault%2fsecret&rtc_password=rtc%20secret&os=linux")
	if err == nil {
		t.Fatal("a dial to a closed port succeeded")
	}
	msg := err.Error()
	for _, secret := range []string{"dialsecret", "vault%2fsecret", "vault%2Fsecret", "vault/secret", "rtc%20secret", "rtc+secret", "rtc secret"} {
		if strings.Contains(msg, secret) {
			t.Errorf("the dial error holds %q: %s", secret, msg)
		}
	}
	if !strings.HasPrefix(msg, "cdp dial: ") || !strings.Contains(msg, "REDACTED") || !strings.Contains(msg, "os=linux") {
		t.Errorf("want the dial error with its URL masked, got %s", msg)
	}
}

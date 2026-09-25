package main

import "testing"

// The discriminator is a wire value: the API matches it against its own registry,
// so the typed constant has to keep serialising to the same bytes.
func TestVaultLinkedServiceWireValue(t *testing.T) {
	if got := string(vaultLinkedServiceOnePassword); got != "1password" {
		t.Fatalf("got %q, want 1password", got)
	}
}

func TestParseVaultLinkedService(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want vaultLinkedService
		ok   bool
	}{
		{"registered", "1password", vaultLinkedServiceOnePassword, true},
		{"unregistered", "bitwarden", "", false},
		{"case is not normalised", "1Password", "", false},
		{"empty is not a discriminator", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseVaultLinkedService(c.raw)
			if c.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

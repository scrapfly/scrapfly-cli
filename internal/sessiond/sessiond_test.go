package sessiond

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_Order(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Explicit flag wins over env and file.
	t.Setenv("SCRAPFLY_SESSION", "env-session")
	if err := os.MkdirAll(filepath.Join(dir, ".scrapfly", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".scrapfly", "sessions", ".current"), []byte("file-session"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, ok := Resolve("flag-session")
	if !ok || id != "flag-session" {
		t.Fatalf("explicit flag must win: %q %v", id, ok)
	}

	id, ok = Resolve("")
	if !ok || id != "env-session" {
		t.Fatalf("env wins over file: %q %v", id, ok)
	}

	t.Setenv("SCRAPFLY_SESSION", "")
	id, ok = Resolve("")
	if !ok || id != "file-session" {
		t.Fatalf("file fallback: %q %v", id, ok)
	}
}

func TestResolve_EmptyWhenNothingSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SCRAPFLY_SESSION", "")
	id, ok := Resolve("")
	if ok || id != "" {
		t.Fatalf("should report empty: %q %v", id, ok)
	}
}

func TestSetClearCurrent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := SetCurrent("sess-42"); err != nil {
		t.Fatal(err)
	}
	if got := ReadCurrent(); got != "sess-42" {
		t.Fatalf("ReadCurrent: %q", got)
	}
	// ClearCurrent is a no-op when the id doesn't match — guard against
	// concurrent daemons clobbering each other's marker.
	if err := ClearCurrent("other"); err != nil {
		t.Fatal(err)
	}
	if got := ReadCurrent(); got != "sess-42" {
		t.Fatalf("Clear should have been a noop: %q", got)
	}
	if err := ClearCurrent("sess-42"); err != nil {
		t.Fatal(err)
	}
	if got := ReadCurrent(); got != "" {
		t.Fatalf("Clear should have removed the file: %q", got)
	}
}

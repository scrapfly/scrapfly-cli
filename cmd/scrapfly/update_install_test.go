package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func tarGzOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// releaseServing serves a release of one archive and its checksums.txt.
func releaseServing(t *testing.T, assetName string, archive []byte) *githubRelease {
	t.Helper()
	sum := sha256.Sum256(archive)
	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + assetName + "\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &githubRelease{
		TagName: "v9.0.0",
		Assets: []githubAsset{
			{Name: assetName, BrowserDownloadURL: srv.URL + "/asset"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}
}

// swapRecorder stands in for swapBinary and keeps what it was handed.
type swapRecorder struct {
	calls   int
	content []byte
}

func (s *swapRecorder) swap(path string) error {
	s.calls++
	content, err := os.ReadFile(path)
	s.content = content
	return err
}

func runningAt(t *testing.T, v string) func() (macOSVersion, error) {
	return func() (macOSVersion, error) { return mustMacOS(t, v), nil }
}

// The update never puts a release macOS would refuse in place of the binary.
func TestInstallReleaseRefusesBeforeTheSwap(t *testing.T) {
	binary := thinMachO(macho.CpuArm64, loadCmdBuildVersion, 0x000d0000)
	rel := releaseServing(t, "scrapfly-darwin-universal.tar.gz", tarGzOf(t, "scrapfly", binary))

	var refused swapRecorder
	err := installReleaseFor(context.Background(), rel, "darwin", "arm64", t.TempDir(), runningAt(t, "12.7.4"), refused.swap)
	if err == nil || !strings.Contains(err.Error(), "v0.4.1 is the last release for macOS 12") {
		t.Errorf("want the macOS 12 refusal, got %v", err)
	}
	if refused.calls != 0 {
		t.Error("the binary was swapped after the refusal")
	}

	var unread swapRecorder
	err = installReleaseFor(context.Background(), rel, "darwin", "arm64", t.TempDir(),
		func() (macOSVersion, error) { return macOSVersion{}, errors.New("no sysctl") }, unread.swap)
	if err == nil || unread.calls != 0 {
		t.Errorf("an unreadable macOS version let the swap run: %v, %d swaps", err, unread.calls)
	}

	var accepted swapRecorder
	if err := installReleaseFor(context.Background(), rel, "darwin", "arm64", t.TempDir(), runningAt(t, "13.5"), accepted.swap); err != nil {
		t.Fatal(err)
	}
	if accepted.calls != 1 || !bytes.Equal(accepted.content, binary) {
		t.Errorf("macOS 13.5: %d swaps of %d bytes, want 1 of the release binary", accepted.calls, len(accepted.content))
	}
}

// Linux and Windows install without asking the macOS version, and a Windows
// .zip is unzipped, not read as a tarball.
func TestInstallReleaseOffMacOS(t *testing.T) {
	notAsked := func() (macOSVersion, error) {
		t.Error("the macOS version was read off macOS")
		return macOSVersion{}, nil
	}

	linuxBinary := []byte("\x7fELF linux binary")
	var linux swapRecorder
	rel := releaseServing(t, "scrapfly-linux-amd64.tar.gz", tarGzOf(t, "scrapfly", linuxBinary))
	if err := installReleaseFor(context.Background(), rel, "linux", "amd64", t.TempDir(), notAsked, linux.swap); err != nil {
		t.Fatal(err)
	}
	if linux.calls != 1 || !bytes.Equal(linux.content, linuxBinary) {
		t.Errorf("linux: %d swaps, content %q", linux.calls, linux.content)
	}

	windowsBinary := []byte("MZ windows binary")
	var windows swapRecorder
	rel = releaseServing(t, "scrapfly-windows-amd64.zip", zipOf(t, "scrapfly.exe", windowsBinary))
	if err := installReleaseFor(context.Background(), rel, "windows", "amd64", t.TempDir(), notAsked, windows.swap); err != nil {
		t.Fatal(err)
	}
	if windows.calls != 1 || !bytes.Equal(windows.content, windowsBinary) {
		t.Errorf("windows: %d swaps, content %q", windows.calls, windows.content)
	}
}

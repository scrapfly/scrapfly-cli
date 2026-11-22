package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestUpdateAcrossFilesystems(t *testing.T) {
	const assetEnv = "SCRAPFLY_TEST_UPDATE_ASSET"
	const checksumEnv = "SCRAPFLY_TEST_UPDATE_CHECKSUMS"
	assetName := "scrapfly-linux-" + runtime.GOARCH + ".tar.gz"

	if assetURL := os.Getenv(assetEnv); assetURL != "" {
		release := &githubRelease{
			TagName: "v9.0.0",
			Assets: []githubAsset{
				{Name: assetName, BrowserDownloadURL: assetURL},
				{Name: "checksums.txt", BrowserDownloadURL: os.Getenv(checksumEnv)},
			},
		}
		if err := installRelease(t.Context(), release); err != nil {
			t.Fatal(err)
		}
		return
	}

	installDir := t.TempDir()
	downloadDir, err := os.MkdirTemp("/dev/shm", "scrapfly-update-test-")
	if err != nil {
		t.Skipf("second filesystem unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(downloadDir); err != nil {
			t.Error(err)
		}
	})
	if deviceID(t, installDir) == deviceID(t, downloadDir) {
		t.Skip("test installation and downloads must be on different filesystems")
	}

	exe := filepath.Join(installDir, "scrapfly")
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(testExe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}

	release := releaseServing(t, assetName, tarGzOf(t, "scrapfly", []byte("#!/bin/sh\necho updated\n")))
	update := exec.CommandContext(t.Context(), exe, "-test.run", "^TestUpdateAcrossFilesystems$")
	update.Env = append(os.Environ(),
		"TMPDIR="+downloadDir,
		assetEnv+"="+release.Assets[0].BrowserDownloadURL,
		checksumEnv+"="+release.Assets[1].BrowserDownloadURL,
	)
	if output, err := update.CombinedOutput(); err != nil {
		t.Fatalf("update failed: %v\n%s", err, output)
	}

	backup, err := os.Stat(exe + ".bak")
	if err != nil || !os.SameFile(original, backup) {
		t.Fatalf("original executable missing from backup: %v", err)
	}
	if output, err := exec.CommandContext(t.Context(), exe).CombinedOutput(); err != nil || string(output) != "updated\n" {
		t.Fatalf("updated binary did not run: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("download directory was not cleaned: %v, %v", entries, err)
	}
	entries, err = os.ReadDir(installDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("installation directory should contain only the binary and backup: %v, %v", entries, err)
	}
}

func deviceID(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stats, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("filesystem device ID unavailable")
	}
	return uint64(stats.Dev)
}

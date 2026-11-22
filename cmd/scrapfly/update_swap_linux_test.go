package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	if err != nil || len(entries) != 3 {
		t.Fatalf("installation directory should contain only the binary, backup and update lock: %v, %v", entries, err)
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

func TestUpdateRejectsRenamedRunningBackup(t *testing.T) {
	const barrierEnv = "SCRAPFLY_TEST_BACKUP_BARRIER"
	if os.Getenv(barrierEnv) == "1" {
		fmt.Println("ready")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil || !strings.HasSuffix(exe, ".bak") {
			t.Fatalf("expected renamed executable: %q %v", exe, err)
		}
		release := &githubRelease{TagName: "v9.0.0", Assets: []githubAsset{
			{Name: "scrapfly-linux-" + runtime.GOARCH + ".tar.gz", BrowserDownloadURL: os.Getenv("SCRAPFLY_TEST_BACKUP_ASSET")},
			{Name: "checksums.txt", BrowserDownloadURL: os.Getenv("SCRAPFLY_TEST_BACKUP_CHECKSUM")},
		}}
		err = installRelease(t.Context(), release)
		if err == nil || !strings.Contains(err.Error(), "cannot update a backup executable") {
			t.Fatalf("expected backup rejection before release processing, got %v", err)
		}
		return
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "scrapfly")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), exe, "-test.run=^TestUpdateRejectsRenamedRunningBackup$")
	release := releaseServing(t, "scrapfly-linux-"+runtime.GOARCH+".tar.gz", tarGzOf(t, "scrapfly", []byte("downloaded successor")))
	child.Env = append(os.Environ(), barrierEnv+"=1",
		"SCRAPFLY_TEST_BACKUP_ASSET="+release.Assets[0].BrowserDownloadURL,
		"SCRAPFLY_TEST_BACKUP_CHECKSUM="+release.Assets[1].BrowserDownloadURL)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = child.Process.Kill() })
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child not ready: %q %v", line, err)
	}
	successor := exe + ".new"
	if err := os.WriteFile(successor, []byte("successor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := swapBinary(exe, successor); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(input, "continue"); err != nil {
		t.Fatal(err)
	}
	// Drain stdout before Wait because the helper writes its test result there.
	var result bytes.Buffer
	if _, err := result.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("child: %v %s %s", err, result.String(), stderr.String())
	}
	for suffix, want := range map[string][]byte{"": []byte("successor"), ".bak": binary, ".update.lock": {}} {
		got, err := os.ReadFile(exe + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("installation changed at %s: %v", suffix, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("unexpected backup output: %v %v", entries, err)
	}
}

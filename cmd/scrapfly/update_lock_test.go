//go:build linux || darwin || windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateLockProcess(t *testing.T) {
	mode := os.Getenv("SCRAPFLY_TEST_LOCK_MODE")
	if mode == "" {
		return
	}
	exe := os.Getenv("SCRAPFLY_TEST_LOCK_EXE")
	if mode == "hold" {
		file, err := os.OpenFile(exe+".update.lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := lockUpdateFile(file); err != nil {
			t.Fatal(err)
		}
		fmt.Println("locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		return
	}
	err := swapBinary(exe, exe+".new")
	if mode == "contend" {
		if err == nil || !strings.Contains(err.Error(), "acquire update lock") {
			t.Fatalf("expected contention, got %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateLockContentionAndRecovery(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "scrapfly")
	for suffix, value := range map[string]string{"": "original", ".bak": "previous", ".new": "successor"} {
		if err := os.WriteFile(exe+suffix, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	process := func(mode string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestUpdateLockProcess$")
		cmd.Env = append(os.Environ(), "SCRAPFLY_TEST_LOCK_MODE="+mode, "SCRAPFLY_TEST_LOCK_EXE="+exe)
		return cmd
	}
	holder := process("hold")
	input, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = holder.Process.Kill(); _ = holder.Wait() })
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("holder not ready: %q %v", line, err)
	}
	if out, err := process("contend").CombinedOutput(); err != nil {
		t.Fatalf("contender: %v %s", err, out)
	}
	assertFile := func(suffix, want string) {
		t.Helper()
		got, err := os.ReadFile(exe + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, want %q: %v", suffix, got, want, err)
		}
	}
	assertFile("", "original")
	assertFile(".bak", "previous")
	assertFile(".new", "successor")
	lockBefore, err := os.Stat(exe + ".update.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	if out, err := process("swap").CombinedOutput(); err != nil {
		t.Fatalf("recovery: %v %s", err, out)
	}
	assertFile("", "successor")
	assertFile(".bak", "original")
	assertFile(".update.lock", "")
	lockAfter, err := os.Stat(exe + ".update.lock")
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("lock identity changed: %v", err)
	}
	// A failed install rolls back while locked and releases the lock for retry.
	if err := swapBinary(exe, exe+".missing"); err == nil {
		t.Fatal("missing successor accepted")
	}
	assertFile("", "successor")
	if err := os.WriteFile(exe+".new", []byte("retry"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := process("swap").CombinedOutput(); err != nil {
		t.Fatalf("retry: %v %s", err, out)
	}
	assertFile("", "retry")
	assertFile(".bak", "successor")
}

func TestUpdateLockCoversOperation(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "scrapfly")
	err := withUpdateLock(exe, func() error {
		second, err := os.OpenFile(exe+".update.lock", os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer second.Close()
		if err := lockUpdateFile(second); err == nil {
			t.Fatal("lock released during operation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdatePreservesBackupWhenExecutableMissing(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "scrapfly")
	for suffix, value := range map[string]string{".bak": "recovery", ".new": "successor"} {
		if err := os.WriteFile(exe+suffix, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := swapBinary(exe, exe+".new"); err == nil {
		t.Fatal("missing executable accepted")
	}
	for suffix, want := range map[string]string{".bak": "recovery", ".new": "successor"} {
		got, err := os.ReadFile(exe + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("%s not preserved: %q %v", suffix, got, err)
		}
	}
}

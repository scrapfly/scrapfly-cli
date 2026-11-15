package main

import (
	"os"
	"os/exec"
	"testing"
)

// runningMacOS answers what sw_vers answers outside SYSTEM_VERSION_COMPAT, on a
// macOS the release supports; CI runs it with SYSTEM_VERSION_COMPAT=1 too.
func TestRunningMacOSReadsThisMac(t *testing.T) {
	got, err := runningMacOS()
	if err != nil {
		t.Fatal(err)
	}
	swVers := exec.Command("/usr/bin/sw_vers", "-productVersion")
	swVers.Env = append(os.Environ(), "SYSTEM_VERSION_COMPAT=0")
	out, err := swVers.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := mustMacOS(t, string(out))
	if got != want {
		t.Errorf("runningMacOS() = %v, sw_vers says %v", got, want)
	}
	if got.less(macOSVersion{major: 13}) {
		t.Errorf("runningMacOS() = %v, below the macOS 13 the release needs", got)
	}

	resolved, err := macOSCompatVersion.resolveCompat(swVersOutsideCompat)
	if err != nil || resolved != want {
		t.Errorf("10.16 resolved to %v, %v; sw_vers says %v", resolved, err, want)
	}
}

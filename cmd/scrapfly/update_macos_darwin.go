package main

import (
	"os"
	"os/exec"
	"syscall"
)

// runningMacOS returns the macOS product version of this Mac.
func runningMacOS() (macOSVersion, error) {
	s, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return macOSVersion{}, err
	}
	v, err := parseMacOSVersion(s)
	if err != nil {
		return macOSVersion{}, err
	}
	return v.resolveCompat(swVersOutsideCompat)
}

// swVersOutsideCompat returns what sw_vers prints with SYSTEM_VERSION_COMPAT=0.
func swVersOutsideCompat() (string, error) {
	cmd := exec.Command("/usr/bin/sw_vers", "-productVersion")
	cmd.Env = append(os.Environ(), "SYSTEM_VERSION_COMPAT=0")
	out, err := cmd.Output()
	return string(out), err
}

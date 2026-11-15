//go:build !darwin

package main

import "errors"

// runningMacOS is only answered on macOS.
func runningMacOS() (macOSVersion, error) {
	return macOSVersion{}, errors.New("not macOS")
}

//go:build !linux && !darwin && !windows

package main

import (
	"fmt"
	"os"
	"runtime"
)

func lockUpdateFile(_ *os.File) error {
	return fmt.Errorf("self-update locking is unsupported on %s", runtime.GOOS)
}

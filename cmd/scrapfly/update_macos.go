package main

import (
	"debug/macho"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Mach-O load commands that carry the oldest macOS a binary starts on.
const (
	loadCmdVersionMinMacOSX = 0x24
	loadCmdBuildVersion     = 0x32
)

// lastReleaseByMacOS names the last release that starts on a macOS major
// below the current minimum.
var lastReleaseByMacOS = map[int]string{
	12: "v0.4.1",
}

// macOSCompatVersion is what macOS 11 and later report to a process started
// with SYSTEM_VERSION_COMPAT=1.
var macOSCompatVersion = macOSVersion{major: 10, minor: 16}

// macOSVersion is a macOS product version, such as 12.7.4.
type macOSVersion struct {
	major, minor, patch int
}

// parseMacOSVersion reads "13", "13.6" or "12.7.4".
func parseMacOSVersion(s string) (macOSVersion, error) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) > 3 {
		return macOSVersion{}, fmt.Errorf("macOS version %q: more than three parts", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return macOSVersion{}, fmt.Errorf("macOS version %q: part %q is not a number", s, p)
		}
		nums[i] = n
	}
	return macOSVersion{major: nums[0], minor: nums[1], patch: nums[2]}, nil
}

// macOSVersionFromMachO decodes the xxxx.yy.zz nibbles of a Mach-O version field.
func macOSVersionFromMachO(packed uint32) macOSVersion {
	return macOSVersion{major: int(packed >> 16), minor: int(packed >> 8 & 0xff), patch: int(packed & 0xff)}
}

func (v macOSVersion) less(o macOSVersion) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	if v.minor != o.minor {
		return v.minor < o.minor
	}
	return v.patch < o.patch
}

func (v macOSVersion) String() string {
	if v.patch == 0 {
		return fmt.Sprintf("%d.%d", v.major, v.minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// resolveCompat replaces the 10.16 a SYSTEM_VERSION_COMPAT=1 process is told
// with the version real reads outside compat mode.
func (v macOSVersion) resolveCompat(real func() (string, error)) (macOSVersion, error) {
	if v != macOSCompatVersion {
		return v, nil
	}
	s, err := real()
	if err != nil {
		return macOSVersion{}, fmt.Errorf("macOS reports 10.16 (SYSTEM_VERSION_COMPAT=1) and its real version could not be read: %w", err)
	}
	return parseMacOSVersion(s)
}

// lastRelease names the last release that starts on this macOS major, "" when
// none is listed.
func (v macOSVersion) lastRelease() string {
	return lastReleaseByMacOS[v.major]
}

// minimumMacOSOf returns the oldest macOS the Mach-O binary at path starts
// on. A universal binary must hold a goarch slice, and its minimum is the
// highest of its slices, the one macOS may start whatever arch installs it
// (Rosetta); an Intel Mac is refused when only the arm64 slice needs a newer
// macOS. found is false when the binary declares none.
func minimumMacOSOf(path, goarch string) (minimum macOSVersion, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return macOSVersion{}, false, err
	}
	defer f.Close()

	fat, err := macho.NewFatFile(f)
	if err == nil {
		defer fat.Close()
		hasArch := false
		for _, a := range fat.Arches {
			if a.Cpu == machoCPU(goarch) {
				hasArch = true
			}
			if sliceMinimum, ok := machOMinimumMacOS(a.File); ok && (!found || minimum.less(sliceMinimum)) {
				minimum, found = sliceMinimum, true
			}
		}
		if !hasArch {
			return macOSVersion{}, false, fmt.Errorf("universal binary has no %s slice", goarch)
		}
		return minimum, found, nil
	}
	if !errors.Is(err, macho.ErrNotFat) {
		return macOSVersion{}, false, err
	}

	thin, err := macho.NewFile(f)
	if err != nil {
		return macOSVersion{}, false, err
	}
	defer thin.Close()
	minimum, found = machOMinimumMacOS(thin)
	return minimum, found, nil
}

// machOMinimumMacOS reads LC_BUILD_VERSION, or LC_VERSION_MIN_MACOSX on an
// older linker's output.
func machOMinimumMacOS(f *macho.File) (macOSVersion, bool) {
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) < 16 {
			continue
		}
		switch f.ByteOrder.Uint32(raw) {
		case loadCmdBuildVersion:
			// cmd, cmdsize, platform, minos, sdk
			return macOSVersionFromMachO(f.ByteOrder.Uint32(raw[12:])), true
		case loadCmdVersionMinMacOSX:
			// cmd, cmdsize, version, sdk
			return macOSVersionFromMachO(f.ByteOrder.Uint32(raw[8:])), true
		}
	}
	return macOSVersion{}, false
}

func machoCPU(goarch string) macho.Cpu {
	switch goarch {
	case "amd64":
		return macho.CpuAmd64
	case "arm64":
		return macho.CpuArm64
	}
	return 0
}

// guardMacOSRelease refuses, on macOS, a release binary that needs a newer
// macOS than running reports. Other systems pass.
func guardMacOSRelease(goos, goarch, binaryPath, tag string, running func() (macOSVersion, error)) error {
	if goos != "darwin" {
		return nil
	}
	here, err := running()
	if err != nil {
		return fmt.Errorf("read the macOS version of this Mac: %w", err)
	}
	return refuseUnsupportedMacOS(binaryPath, goarch, tag, here)
}

// refuseUnsupportedMacOS refuses a release binary that needs a newer macOS
// than running.
func refuseUnsupportedMacOS(binaryPath, goarch, tag string, running macOSVersion) error {
	minimum, found, err := minimumMacOSOf(binaryPath, goarch)
	if err != nil {
		return fmt.Errorf("read the macOS version %s needs: %w", tag, err)
	}
	if !found || !running.less(minimum) {
		return nil
	}
	refusal := fmt.Sprintf("%s needs macOS %s or later and this Mac runs macOS %s, so the binary was not replaced", tag, minimum, running)
	last := running.lastRelease()
	if last == "" {
		return fmt.Errorf("%s; keep v%s, which runs here", refusal, normalizeVersion(version))
	}
	return fmt.Errorf("%s; %s is the last release for macOS %d, to install it: "+
		"curl -fsSL https://scrapfly.io/scrapfly-cli/install | sh -s -- --version %s", refusal, last, running.major, last)
}

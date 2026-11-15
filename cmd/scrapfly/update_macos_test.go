package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSDK is the SDK every fixture declares, above every minimum they declare.
const fixtureSDK = 0x000f0000

// thinMachO is a 64-bit Mach-O executable header whose only load command
// declares minos through cmd (no command when cmd is 0).
func thinMachO(cpu macho.Cpu, cmd uint32, minos uint32) []byte {
	le := binary.LittleEndian
	var lc []byte
	switch cmd {
	case loadCmdBuildVersion:
		lc = make([]byte, 24)
		le.PutUint32(lc[0:], cmd)
		le.PutUint32(lc[4:], 24)
		le.PutUint32(lc[8:], 1) // PLATFORM_MACOS
		le.PutUint32(lc[12:], minos)
		le.PutUint32(lc[16:], fixtureSDK)
	case loadCmdVersionMinMacOSX:
		lc = make([]byte, 16)
		le.PutUint32(lc[0:], cmd)
		le.PutUint32(lc[4:], 16)
		le.PutUint32(lc[8:], minos)
		le.PutUint32(lc[12:], fixtureSDK)
	}
	ncmds := uint32(0)
	if lc != nil {
		ncmds = 1
	}
	header := make([]byte, 32)
	le.PutUint32(header[0:], macho.Magic64)
	le.PutUint32(header[4:], uint32(cpu))
	le.PutUint32(header[12:], uint32(macho.TypeExec))
	le.PutUint32(header[16:], ncmds)
	le.PutUint32(header[20:], uint32(len(lc)))
	return append(header, lc...)
}

// fatMachO packs thin images into a universal binary, one per page.
func fatMachO(cpus []macho.Cpu, images [][]byte) []byte {
	be := binary.BigEndian
	const page = 4096
	var b bytes.Buffer
	head := make([]byte, 8+20*len(images))
	be.PutUint32(head[0:], macho.MagicFat)
	be.PutUint32(head[4:], uint32(len(images)))
	for i, img := range images {
		entry := head[8+20*i:]
		be.PutUint32(entry[0:], uint32(cpus[i]))
		be.PutUint32(entry[8:], uint32(page*(i+1)))
		be.PutUint32(entry[12:], uint32(len(img)))
		be.PutUint32(entry[16:], 12) // 2^12 alignment
	}
	b.Write(head)
	for i, img := range images {
		b.Write(make([]byte, page*(i+1)-b.Len()))
		b.Write(img)
	}
	return b.Bytes()
}

func writeBinary(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scrapfly")
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustMacOS(t *testing.T, s string) macOSVersion {
	t.Helper()
	v, err := parseMacOSVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMacOSVersionParsesAndOrders(t *testing.T) {
	parsed := []struct {
		in   string
		want macOSVersion
	}{
		{"12.7.4", macOSVersion{12, 7, 4}},
		{"13.6", macOSVersion{13, 6, 0}},
		{"14", macOSVersion{14, 0, 0}},
		{" 15.1\n", macOSVersion{15, 1, 0}},
	}
	for _, c := range parsed {
		if got := mustMacOS(t, c.in); got != c.want {
			t.Errorf("parseMacOSVersion(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "13.x", "13.0.1.2", "-1.0"} {
		if _, err := parseMacOSVersion(bad); err == nil {
			t.Errorf("parseMacOSVersion(%q) accepted", bad)
		}
	}

	ordered := []struct {
		a, b string
		less bool
	}{
		{"12.7.4", "13.0", true},
		{"13.0", "13.0", false},
		{"13.0.1", "13.0", false},
		{"13.9.9", "14.0", true},
		{"14.0", "13.9.9", false},
		{"13.1", "13.10", true},
	}
	for _, c := range ordered {
		if got := mustMacOS(t, c.a).less(mustMacOS(t, c.b)); got != c.less {
			t.Errorf("%s < %s = %v, want %v", c.a, c.b, got, c.less)
		}
	}

	if got := macOSVersionFromMachO(0x000d0000); got != (macOSVersion{13, 0, 0}) {
		t.Errorf("0x000d0000 = %v, want 13.0", got)
	}
	if got := macOSVersionFromMachO(0x000c0704); got != (macOSVersion{12, 7, 4}) {
		t.Errorf("0x000c0704 = %v, want 12.7.4", got)
	}
}

// A release built for macOS 13 is refused on macOS 12 with the release to
// stay on and the installer line that pins it.
func TestUpdateRefusesAReleaseThisMacCannotStart(t *testing.T) {
	path := writeBinary(t, thinMachO(macho.CpuArm64, loadCmdBuildVersion, 0x000d0000))

	err := refuseUnsupportedMacOS(path, "arm64", "v0.5.0", mustMacOS(t, "12.7.4"))
	if err == nil {
		t.Fatal("a macOS 13 binary was accepted on macOS 12.7.4")
	}
	for _, want := range []string{"v0.5.0 needs macOS 13.0", "runs macOS 12.7.4", "not replaced", "v0.4.1 is the last release for macOS 12", "install | sh -s -- --version v0.4.1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}

	for _, running := range []string{"13.0", "13.6.1", "15.2"} {
		if err := refuseUnsupportedMacOS(path, "arm64", "v0.5.0", mustMacOS(t, running)); err != nil {
			t.Errorf("macOS %s refused a macOS 13 binary: %v", running, err)
		}
	}
}

// A universal binary needs the highest minimum of its slices, whatever arch
// installs it: under Rosetta an amd64 process installs what macOS starts as
// arm64.
func TestUpdateJudgesAUniversalBinaryByItsHighestSlice(t *testing.T) {
	path := writeBinary(t, fatMachO(
		[]macho.Cpu{macho.CpuAmd64, macho.CpuArm64},
		[][]byte{
			thinMachO(macho.CpuAmd64, loadCmdBuildVersion, 0x000d0000),
			thinMachO(macho.CpuArm64, loadCmdBuildVersion, 0x000e0000),
		},
	))

	for _, goarch := range []string{"amd64", "arm64"} {
		err := refuseUnsupportedMacOS(path, goarch, "v9.0.0", mustMacOS(t, "13.5"))
		if err == nil || !strings.Contains(err.Error(), "needs macOS 14.0") {
			t.Errorf("%s on macOS 13.5: want the arm64 slice's 14.0 named, got %v", goarch, err)
		}
		if err := refuseUnsupportedMacOS(path, goarch, "v9.0.0", mustMacOS(t, "14.1")); err != nil {
			t.Errorf("%s on macOS 14.1 was refused: %v", goarch, err)
		}
	}
	if err := refuseUnsupportedMacOS(path, "386", "v9.0.0", mustMacOS(t, "14.1")); err == nil {
		t.Error("a universal binary without this arch was accepted")
	}
}

// SYSTEM_VERSION_COMPAT=1 makes macOS 11 and later say 10.16: the real
// version is read instead, and only then.
func TestMacOSCompatVersionIsReadAgain(t *testing.T) {
	got, err := mustMacOS(t, "10.16").resolveCompat(func() (string, error) { return "13.6.1\n", nil })
	if err != nil || got != (macOSVersion{13, 6, 1}) {
		t.Errorf("10.16 resolved to %v, %v; want 13.6.1", got, err)
	}

	got, err = mustMacOS(t, "12.7.4").resolveCompat(func() (string, error) {
		t.Error("a real version was read again")
		return "", nil
	})
	if err != nil || got != (macOSVersion{12, 7, 4}) {
		t.Errorf("12.7.4 resolved to %v, %v", got, err)
	}

	if _, err := mustMacOS(t, "10.16").resolveCompat(func() (string, error) { return "", os.ErrNotExist }); err == nil {
		t.Error("an unreadable real version was accepted")
	}
}

// A macOS with no listed last release is told to keep the running one.
func TestUpdateRefusalKeepsTheRunningReleaseWhenNoneIsListed(t *testing.T) {
	path := writeBinary(t, thinMachO(macho.CpuAmd64, loadCmdVersionMinMacOSX, 0x000c0000))

	err := refuseUnsupportedMacOS(path, "amd64", "v9.0.0", mustMacOS(t, "11.7"))
	keep := "keep v" + normalizeVersion(version) + ", which runs here"
	if err == nil || !strings.Contains(err.Error(), "needs macOS 12.0") || !strings.Contains(err.Error(), keep) {
		t.Errorf("want %q, got %v", keep, err)
	}
}

func TestUpdateKeepsBinariesWithoutAMinimumAndRefusesUnreadableOnes(t *testing.T) {
	bare := writeBinary(t, thinMachO(macho.CpuArm64, 0, 0))
	if err := refuseUnsupportedMacOS(bare, "arm64", "v9.0.0", mustMacOS(t, "12.0")); err != nil {
		t.Errorf("a binary that declares no minimum was refused: %v", err)
	}

	garbage := writeBinary(t, []byte("not a Mach-O binary"))
	if err := refuseUnsupportedMacOS(garbage, "arm64", "v9.0.0", mustMacOS(t, "15.0")); err == nil {
		t.Error("an unreadable binary was accepted")
	}
}

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// Auto-update plumbing: GitHub releases are the source of truth for
// the binary (naming: `scrapfly-<os>-<arch>.tar.gz` + `checksums.txt`,
// see .goreleaser.yaml). The release-notes RSS feed at
// {{ public_api_endpoint }}/docs/release-notes/feed.xml is used only
// as an optional "human-readable changelog" render target — the binary
// and its checksum always come from the GitHub release.

const (
	githubRepo         = "scrapfly/scrapfly-cli"
	githubAPI          = "https://api.github.com/repos/" + githubRepo + "/releases/latest"
	updateCheckCacheTTL = 24 * time.Hour
)

type githubRelease struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	HTMLURL string         `json:"html_url"`
	Assets  []githubAsset  `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
	HTMLURL   string    `json:"html_url"`
}

func newUpdateCmd(flags *rootFlags) *cobra.Command {
	var (
		check bool
		yes   bool
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install a newer Scrapfly CLI release",
		Long: `Check GitHub Releases for a newer version of the CLI and, unless --check
is passed, download + verify + replace the currently running binary.

The update is checksum-verified (checksums.txt from the release), atomic
(new binary written to a temp file next to the current one, then renamed),
and reversible (the previous binary is kept as .bak for one cycle).`,
		Example: `  scrapfly update --check       # just report whether a newer version exists
  scrapfly update --yes          # install silently
  scrapfly update                 # prompt for confirmation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			rel, err := fetchLatestRelease(ctx)
			if err != nil {
				return fmt.Errorf("fetch latest release: %w", err)
			}
			latest := normalizeVersion(rel.TagName)
			current := normalizeVersion(version)
			newer := semverGreater(latest, current)

			if !newer {
				if flags.pretty {
					out.Pretty(os.Stdout, "up to date (current %s, latest %s)", current, latest)
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "update", map[string]any{
					"current":   current,
					"latest":    latest,
					"newer":     false,
					"html_url":  rel.HTMLURL,
				})
			}

			if check {
				msg := fmt.Sprintf("newer version available: %s (current %s). Run `scrapfly update` to install.", latest, current)
				if flags.pretty {
					out.Pretty(os.Stdout, "%s", msg)
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "update", map[string]any{
					"current":  current,
					"latest":   latest,
					"newer":    true,
					"html_url": rel.HTMLURL,
				})
			}

			if !yes {
				fmt.Fprintf(os.Stderr, "New version %s available (current %s).\nRelease notes: %s\n\nInstall? [y/N] ", latest, current, rel.HTMLURL)
				var reply string
				_, _ = fmt.Fscanln(os.Stdin, &reply)
				reply = strings.TrimSpace(strings.ToLower(reply))
				if reply != "y" && reply != "yes" {
					return fmt.Errorf("update cancelled")
				}
			}

			if err := installRelease(ctx, rel); err != nil {
				return fmt.Errorf("install %s: %w", latest, err)
			}

			if flags.pretty {
				out.Pretty(os.Stdout, "updated to %s (was %s)", latest, current)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "update", map[string]any{
				"current":  current,
				"latest":   latest,
				"newer":    true,
				"html_url": rel.HTMLURL,
				"installed": true,
			})
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available; do not install")
	cmd.Flags().BoolVar(&yes, "yes", false, "install without prompting for confirmation")
	return cmd
}

func fetchLatestRelease(ctx context.Context) (*githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "scrapfly-cli-updater/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		excerpt := string(body)
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "…"
		}
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, excerpt)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("empty tag_name in release response")
	}
	return &rel, nil
}

// installRelease downloads the archive for the current os/arch, verifies
// its sha256 against checksums.txt, extracts the `scrapfly` binary, and
// atomically replaces the currently running executable.
func installRelease(ctx context.Context, rel *githubRelease) error {
	candidates := assetCandidates(runtime.GOOS, runtime.GOARCH)
	var archive, checksums *githubAsset
	for i := range rel.Assets {
		a := &rel.Assets[i]
		if a.Name == "checksums.txt" {
			checksums = a
			continue
		}
		if archive != nil {
			continue
		}
		for _, want := range candidates {
			if a.Name == want {
				archive = a
				break
			}
		}
	}
	if archive == nil {
		return fmt.Errorf("no release asset matching %v in %s", candidates, rel.TagName)
	}
	if checksums == nil {
		return fmt.Errorf("release %s is missing checksums.txt", rel.TagName)
	}

	expectedSum, err := fetchChecksum(ctx, checksums.BrowserDownloadURL, archive.Name)
	if err != nil {
		return err
	}

	tmpArchive, err := downloadToTemp(ctx, archive.BrowserDownloadURL)
	if err != nil {
		return err
	}
	defer os.Remove(tmpArchive)

	if err := verifySHA256(tmpArchive, expectedSum); err != nil {
		return fmt.Errorf("checksum mismatch on %s: %w", archive.Name, err)
	}

	extracted, err := extractBinary(tmpArchive)
	if err != nil {
		return err
	}
	defer os.Remove(extracted)

	return swapBinary(extracted)
}

func fetchChecksum(ctx context.Context, url, filename string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "scrapfly-cli-updater/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == filename || strings.HasSuffix(fields[1], "/"+filename) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in checksums.txt", filename)
}

func downloadToTemp(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "scrapfly-cli-updater/"+version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s returned %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "scrapfly-update-*.tar.gz")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("expected %s, got %s", expected, got)
	}
	return nil
}

// assetCandidates returns the list of release-asset names to try for a
// given OS/arch, in preference order. Matches the .goreleaser.yaml +
// .goreleaser-linux-windows.yaml naming: `scrapfly-{os}-{arch}.tar.gz`
// for unix-like targets, `.zip` for windows. macOS also publishes a
// universal binary which is preferred when present.
func assetCandidates(osName, arch string) []string {
	switch osName {
	case "darwin":
		return []string{
			"scrapfly-darwin-universal.tar.gz",
			fmt.Sprintf("scrapfly-darwin-%s.tar.gz", arch),
		}
	case "windows":
		return []string{fmt.Sprintf("scrapfly-windows-%s.zip", arch)}
	default:
		return []string{fmt.Sprintf("scrapfly-%s-%s.tar.gz", osName, arch)}
	}
}

// extractBinary opens the release archive (tar.gz for unix, zip for
// windows) and writes the `scrapfly` entry to a temp file, returning
// its path. Caller owns cleanup.
func extractBinary(archivePath string) (string, error) {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZipBinary(archivePath)
	}
	return extractTarGzBinary(archivePath)
}

func extractTarGzBinary(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("archive does not contain a `scrapfly` binary")
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		base := filepath.Base(hdr.Name)
		if base != "scrapfly" && base != "scrapfly.exe" {
			continue
		}
		return writeTempBinary(tr)
	}
}

func extractZipBinary(archivePath string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if base != "scrapfly" && base != "scrapfly.exe" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		path, err := writeTempBinary(rc)
		rc.Close()
		return path, err
	}
	return "", fmt.Errorf("zip does not contain a `scrapfly` binary")
}

func writeTempBinary(src io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "scrapfly-new-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// swapBinary atomically replaces the currently running executable. The
// previous binary is kept as `<exe>.bak` so a failed rollout can be
// recovered with a single `mv`.
func swapBinary(newBinary string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve symlinks so we write to the real binary, not a homebrew
	// wrapper that points at it.
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}

	backup := exe + ".bak"
	// Best-effort: drop any previous .bak so the rename below succeeds
	// on systems where rename over an existing file is disallowed.
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		return fmt.Errorf("back up current binary: %w", err)
	}
	if err := os.Rename(newBinary, exe); err != nil {
		// Best-effort rollback.
		_ = os.Rename(backup, exe)
		return fmt.Errorf("install new binary: %w", err)
	}
	return nil
}

// normalizeVersion strips a leading "v" and any whitespace so version
// comparisons work with both "0.2.0" and "v0.2.0".
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	return v
}

// semverGreater reports whether a > b under a relaxed semver compare
// (x.y.z only; pre-release suffixes are ignored — GitHub tags on this
// project follow plain MAJOR.MINOR.PATCH).
func semverGreater(a, b string) bool {
	ap := parseSemver(a)
	bp := parseSemver(b)
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] > bp[i]
		}
	}
	return false
}

func parseSemver(v string) [3]int {
	parts := strings.SplitN(v, "-", 2) // drop pre-release suffix
	seg := strings.Split(parts[0], ".")
	var out [3]int
	for i := 0; i < 3 && i < len(seg); i++ {
		var n int
		_, _ = fmt.Sscanf(seg[i], "%d", &n)
		out[i] = n
	}
	return out
}

// ── Non-blocking nag on `--version` ─────────────────────────────
// Runs at most once per updateCheckCacheTTL; failures are silent.
// Result is stashed in the user's config dir next to config.json.

func maybeUpdateNag() string {
	cachePath := updateCheckCachePath()
	if cachePath == "" {
		return ""
	}
	cache, _ := readUpdateCache(cachePath)
	if cache != nil && time.Since(cache.CheckedAt) < updateCheckCacheTTL {
		if cache.LatestTag != "" && semverGreater(normalizeVersion(cache.LatestTag), normalizeVersion(version)) {
			return fmt.Sprintf("newer version available: %s — run `scrapfly update` (release notes: %s)",
				cache.LatestTag, cache.HTMLURL)
		}
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rel, err := fetchLatestRelease(ctx)
	if err != nil {
		return "" // silent failure — don't block `--version` on network
	}
	_ = writeUpdateCache(cachePath, &updateCache{
		CheckedAt: time.Now(),
		LatestTag: rel.TagName,
		HTMLURL:   rel.HTMLURL,
	})
	if semverGreater(normalizeVersion(rel.TagName), normalizeVersion(version)) {
		return fmt.Sprintf("newer version available: %s — run `scrapfly update` (release notes: %s)",
			rel.TagName, rel.HTMLURL)
	}
	return ""
}

func updateCheckCachePath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "scrapfly-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return filepath.Join(dir, "update-check.json")
}

func readUpdateCache(path string) (*updateCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func writeUpdateCache(path string, c *updateCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

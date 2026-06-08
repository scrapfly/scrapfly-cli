package main

// Scrapfly Exit Peer (BYOP connector) subcommands.
//
//   scrapfly exit-peer install   Download the connector + cert bundle for the local platform
//   scrapfly exit-peer run       Run the installed connector in the foreground
//   scrapfly exit-peer status    Show the install dir + cert lifetime
//
// All three commands share an install dir resolved in this order:
//   1. --dir flag
//   2. $SCRAPFLY_BYOP_DIR
//   3. $HOME/.scrapfly/byop
//
// The install path mirrors the curl-pipe-sh installer at
// {host}/install/byop-connector/{os}/{arch}?api_key=... so customers can
// pick whichever workflow (CLI vs. shell one-liner) they prefer.

import (
	"archive/zip"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

const (
	exitPeerBinaryBase = "scrapfly-byop-connector"
	exitPeerCertFile   = "connector.crt"
	exitPeerKeyFile    = "connector.key"
	exitPeerCAFile     = "ca.crt"
)

func newExitPeerCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exit-peer",
		Short: "Manage a Scrapfly BYOP (Bring Your Own Proxy) Exit Peer connector",
		Long: `Drive the local scrapfly-byop-connector binary that turns this machine
into an egress for your RPA workflows.

The connector dials home over mTLS to Scrapfly's coordinator and stays
attached. Any RPA Profile (kind=byop) bound to it routes browser
traffic out through this machine's public IP. Use when a target site
allow-lists customer IPs, when geo-pinning egress, or for HIPAA flows
where payload must stay inside your network boundary.

Subcommands:
  install   Download the connector + freshly minted 7-day cert
  run       Run the connector in the foreground (Ctrl-C to stop)
  status    Print install dir + cert lifetime`,
		Example: `  scrapfly exit-peer install
  scrapfly exit-peer run --name my-machine
  scrapfly exit-peer status`,
	}

	cmd.AddCommand(newExitPeerInstallCmd(flags))
	cmd.AddCommand(newExitPeerRunCmd(flags))
	cmd.AddCommand(newExitPeerStatusCmd(flags))
	return cmd
}

// installDir resolves the directory the connector lives in. Override
// order: --dir flag, $SCRAPFLY_BYOP_DIR, $HOME/.scrapfly/byop.
func installDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("SCRAPFLY_BYOP_DIR"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve $HOME: %w", err)
	}
	return filepath.Join(home, ".scrapfly", "byop"), nil
}

// binaryPath returns the platform-correct binary basename inside dir.
func binaryPath(dir string) string {
	name := exitPeerBinaryBase
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func newExitPeerInstallCmd(flags *rootFlags) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download the connector binary + per-customer cert bundle",
		Long: `Download the BYOP connector + a freshly minted 7-day mTLS client
certificate for this account, then unpack into a local install dir.

The remote endpoint mints a per-customer ECDSA P-256 cert whose
Subject OU is your user_uuid. The Scrapfly coordinator uses the OU to
route SOCKS5 traffic only to connectors owned by the matching account.

Re-running install rotates the cert (lifetime is 7 days; rotate
proactively).`,
		Example: `  scrapfly exit-peer install
  scrapfly exit-peer install --dir /opt/scrapfly/byop`,
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKey, host, err := resolveAuthAndHost(flags)
			if err != nil {
				return err
			}

			target, err := installDir(dir)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create install dir %s: %w", target, err)
			}

			osName := runtime.GOOS
			if osName == "darwin" || osName == "linux" || osName == "windows" {
				// supported
			} else {
				return fmt.Errorf("unsupported OS %q (linux/darwin/windows only)", osName)
			}
			archName := runtime.GOARCH
			if archName != "amd64" && archName != "arm64" {
				return fmt.Errorf("unsupported arch %q (amd64/arm64 only)", archName)
			}

			zipBytes, certNotAfter, err := downloadBundle(host, apiKey, osName, archName, flags.insecure, flags.timeout)
			if err != nil {
				return err
			}
			if err := unpackBundle(zipBytes, target); err != nil {
				return err
			}

			if flags.pretty {
				out.Pretty(os.Stdout, "installed connector to %s (cert valid until %s)", target, certNotAfter)
				out.Pretty(os.Stdout, "next: scrapfly exit-peer run --name $(hostname)")
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "exit-peer.install", map[string]any{
				"install_dir":    target,
				"cert_not_after": certNotAfter,
				"os":             osName,
				"arch":           archName,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "install dir (default $SCRAPFLY_BYOP_DIR or $HOME/.scrapfly/byop)")
	return cmd
}

func newExitPeerRunCmd(flags *rootFlags) *cobra.Command {
	var (
		dir         string
		name        string
		rendezvous  string
		allow       []string
		tags        []string
		metricsAddr string
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the installed connector in the foreground",
		Long: `Launch the local connector binary against the Scrapfly coordinator.
Blocks until the process exits (Ctrl-C stops it). On startup the connector
dials home over mTLS, registers, and prints a short_id you can plug into
an RPA Profile.

Run install first; this command does not download anything.`,
		Example: `  scrapfly exit-peer run --name my-machine
  scrapfly exit-peer run --name eu-edge --tag zone=eu --allow=*.example.com`,
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKey, _, err := resolveAuthAndHost(flags)
			if err != nil {
				return err
			}

			target, err := installDir(dir)
			if err != nil {
				return err
			}
			bin := binaryPath(target)
			if _, err := os.Stat(bin); err != nil {
				return fmt.Errorf("connector binary missing at %s, run `scrapfly exit-peer install` first", bin)
			}

			if name == "" {
				h, _ := os.Hostname()
				if h == "" {
					h = "my-machine"
				}
				name = h
			}

			runArgs := []string{
				"--api-key=" + apiKey,
				"--name=" + name,
				"--cert=" + filepath.Join(target, exitPeerCertFile),
				"--key=" + filepath.Join(target, exitPeerKeyFile),
				"--ca=" + filepath.Join(target, exitPeerCAFile),
			}
			if rendezvous != "" {
				runArgs = append(runArgs, "--rendezvous="+rendezvous)
			}
			for _, a := range allow {
				runArgs = append(runArgs, "--allow="+a)
			}
			for _, t := range tags {
				runArgs = append(runArgs, "--tag", t)
			}
			if metricsAddr != "" {
				runArgs = append(runArgs, "--metrics-addr="+metricsAddr)
			}

			c := exec.CommandContext(cmd.Context(), bin, runArgs...)
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Stdin = os.Stdin
			if err := c.Run(); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "install dir (default $SCRAPFLY_BYOP_DIR or $HOME/.scrapfly/byop)")
	cmd.Flags().StringVar(&name, "name", "", "stable label for this connector (defaults to $HOSTNAME)")
	cmd.Flags().StringVar(&rendezvous, "rendezvous", "", "override coordinator host:port (advanced)")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "egress allow-list entry (domain or CIDR), repeatable")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "tag in k=v form (e.g. zone=eu), repeatable")
	cmd.Flags().StringVar(&metricsAddr, "metrics-addr", "", "optional listen addr for /metrics (e.g. 127.0.0.1:9100)")
	return cmd
}

func newExitPeerStatusCmd(flags *rootFlags) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local install state + cert lifetime",
		Long:  `Inspect the install dir, confirm the binary is present, and parse the cert to print its expiry.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := installDir(dir)
			if err != nil {
				return err
			}

			st := map[string]any{
				"install_dir":      target,
				"binary_present":   false,
				"cert_present":     false,
				"cert_not_after":   "",
				"cert_subject_ou":  "",
				"cert_days_remain": 0,
			}

			if _, err := os.Stat(binaryPath(target)); err == nil {
				st["binary_present"] = true
			}
			certPath := filepath.Join(target, exitPeerCertFile)
			if data, err := os.ReadFile(certPath); err == nil {
				st["cert_present"] = true
				if block, _ := pem.Decode(data); block != nil && block.Type == "CERTIFICATE" {
					if cert, perr := x509.ParseCertificate(block.Bytes); perr == nil {
						st["cert_not_after"] = cert.NotAfter.UTC().Format(time.RFC3339)
						if ous := cert.Subject.OrganizationalUnit; len(ous) > 0 {
							st["cert_subject_ou"] = ous[0]
						}
						days := int(time.Until(cert.NotAfter).Hours() / 24)
						st["cert_days_remain"] = days
					}
				}
			}

			if flags.pretty {
				out.Pretty(os.Stdout,
					"install_dir=%s binary=%v cert=%v not_after=%s days_remain=%d ou=%s",
					st["install_dir"], st["binary_present"], st["cert_present"],
					st["cert_not_after"], st["cert_days_remain"], st["cert_subject_ou"],
				)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "exit-peer.status", st)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "install dir (default $SCRAPFLY_BYOP_DIR or $HOME/.scrapfly/byop)")
	return cmd
}

// resolveAuthAndHost reads api-key + host from flags / env, with the
// same precedence the rest of the CLI uses (flag overrides env).
// Returns "host" as a clean origin like "https://api.scrapfly.io".
func resolveAuthAndHost(flags *rootFlags) (apiKey, host string, err error) {
	apiKey = flags.apiKey
	if apiKey == "" {
		apiKey = os.Getenv("SCRAPFLY_API_KEY")
	}
	if apiKey == "" {
		return "", "", errors.New("missing api key: set SCRAPFLY_API_KEY or pass --api-key")
	}
	host = strings.TrimRight(flags.host, "/")
	if host == "" {
		host = strings.TrimRight(os.Getenv("SCRAPFLY_HOST"), "/")
	}
	if host == "" {
		host = "https://scrapfly.io"
	}
	return apiKey, host, nil
}

// downloadBundle fetches the per-platform zip from
// {host}/install/byop-connector/{os}/{arch}?api_key=...
// On success returns (zipBytes, certNotAfter, nil). The not_after is
// taken from the X-Cert-Not-After response header (RFC3339).
func downloadBundle(host, apiKey, os, arch string, insecure bool, timeout time.Duration) ([]byte, string, error) {
	url := fmt.Sprintf("%s/install/byop-connector/%s/%s?api_key=%s", host, os, arch, apiKey)

	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	c := &http.Client{Timeout: timeout, Transport: tr}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/zip")
	req.Header.Set("User-Agent", "scrapfly-cli/exit-peer")

	resp, err := c.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, "", fmt.Errorf("bundle endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read bundle: %w", err)
	}
	return buf, resp.Header.Get("X-Cert-Not-After"), nil
}

// unpackBundle extracts the zip into dir. Sets +x on the binary.
func unpackBundle(zipBytes []byte, dir string) error {
	r, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, f := range r.File {
		// Avoid zip-slip: refuse any entry whose cleaned path escapes dir.
		dest := filepath.Join(dir, filepath.Clean(f.Name))
		if !strings.HasPrefix(dest, filepath.Clean(dir)+string(os.PathSeparator)) && dest != filepath.Clean(dir) {
			return fmt.Errorf("zip entry escapes install dir: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(filepath.Base(f.Name), exitPeerBinaryBase) {
			mode = 0o755
		} else if filepath.Base(f.Name) == exitPeerKeyFile {
			mode = 0o600
		}
		w, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("create %s: %w", dest, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			_ = rc.Close()
			_ = w.Close()
			return fmt.Errorf("write %s: %w", dest, err)
		}
		_ = rc.Close()
		_ = w.Close()
	}
	return nil
}

package main

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// newDownloadCmd wraps `scrape` for binary resources (PDFs, images, ZIPs, ...).
// The API returns format="binary" with base64-encoded content; this command
// decodes and writes the raw bytes to -o / -O / auto-named.
func newDownloadCmd(flags *rootFlags) *cobra.Command {
	var (
		country  string
		asp      bool
		renderJS bool
	)
	cmd := &cobra.Command{
		Use:   "download <url>",
		Short: "Download a binary file (PDF, image, ZIP, ...) via the Scraping API",
		Long: `Fetches the URL through Scrapfly's scraping API and writes the decoded
binary body to disk. Equivalent to scrape + base64-decode for files where
format="binary".

Output path:
  -o <file>   write to this exact path
  -O <dir>    auto-name from the URL (e.g. eula.pdf)
  (neither)   auto-name into the current directory`,
		Example: `  scrapfly download https://web-scraping.dev/assets/pdf/eula.pdf -o eula.pdf
  scrapfly download https://example.com/report.pdf -O ./downloads/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			applyProductDefaults(cmd, map[string]*string{
				"country": &country,
			}, map[string]*bool{
				"asp": &asp, "render-js": &renderJS,
			})
			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			cfg := &scrapfly.ScrapeConfig{
				URL:      args[0],
				Country:  country,
				ASP:      asp,
				RenderJS: renderJS,
				Retry:    true,
			}
			result, err := client.Scrape(cfg)
			if err != nil {
				return err
			}

			content := result.Result.Content
			var data []byte
			if result.Result.Format == "binary" {
				decoded, err := base64.StdEncoding.DecodeString(content)
				if err != nil {
					return fmt.Errorf("base64 decode: %w", err)
				}
				data = decoded
			} else {
				data = []byte(content)
			}

			dst, err := resolveDownloadPath(flags, args[0])
			if err != nil {
				return err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "download", map[string]any{
				"url":    args[0],
				"path":   dst,
				"bytes":  len(data),
				"format": result.Result.Format,
				"status": result.Result.StatusCode,
			})
		},
	}
	cmd.Flags().StringVar(&country, "country", "", "proxy country")
	cmd.Flags().BoolVar(&asp, "asp", false, "anti-bot bypass")
	cmd.Flags().BoolVar(&renderJS, "render-js", false, "render with headless browser first")
	return cmd
}

// resolveDownloadPath picks where to write:
//
//	-o <file> -> exact path
//	-O <dir>  -> dir + filename from URL
//	(neither) -> filename from URL in cwd
func resolveDownloadPath(flags *rootFlags, rawURL string) (string, error) {
	if flags.outputPath != "" {
		return flags.outputPath, nil
	}
	name := filenameFromURL(rawURL)
	if flags.outputDir != "" {
		if err := os.MkdirAll(flags.outputDir, 0o755); err != nil {
			return "", err
		}
		return path.Join(flags.outputDir, name), nil
	}
	return name, nil
}

func filenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "download.bin"
	}
	base := path.Base(u.Path)
	if base == "" || base == "/" || base == "." {
		return "download.bin"
	}
	// Strip query params from the filename.
	if i := strings.IndexByte(base, '?'); i >= 0 {
		base = base[:i]
	}
	return base
}

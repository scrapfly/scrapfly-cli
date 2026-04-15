package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"
)

// newBrowserDownloadCmd downloads a file through the browser session.
// Tries the ScrapiumBrowser download domain first (handles click-triggered
// downloads natively), then falls back to fetch() for URLs that Chrome
// renders inline (PDFs, images, etc.).
func newBrowserDownloadCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download <url>",
		Short: "Download a file via the browser session (ScrapiumBrowser domain + fetch() fallback)",
		Long: `Navigates to the URL. If Chrome triggers a native download, pulls the
file via ScrapiumBrowser.getDownloads. If the URL is rendered inline
(PDFs, images), falls back to fetch() in the browser context.

Output: -o <file>, -O <dir>, or auto-named from the URL.`,
		Example: `  scrapfly browser download https://web-scraping.dev/assets/pdf/eula.pdf -o eula.pdf
  scrapfly browser download https://example.com/report.zip -O ./downloads/`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sid, ok := sessiond.Resolve(sessionIDFlag)
			if !ok {
				return fmt.Errorf("no active session")
			}
			resp, err := sessiond.Send(sid, sessiond.Request{Action: "download", URL: args[0]})
			if err != nil {
				return err
			}
			var raw map[string]any
			_ = json.Unmarshal(resp.Data, &raw)

			method, _ := raw["method"].(string)

			switch method {
			case "scrapium":
				// ScrapiumBrowser path: files map {filename: base64}.
				filesRaw, _ := raw["files"].(map[string]any)
				if len(filesRaw) == 0 {
					return fmt.Errorf("no files in ScrapiumBrowser response")
				}
				var results []map[string]any
				for name, b64raw := range filesRaw {
					b64, _ := b64raw.(string)
					data, err := base64.StdEncoding.DecodeString(b64)
					if err != nil {
						return fmt.Errorf("decode %s: %w", name, err)
					}
					dst := resolveBrowserDownloadPath(flags, name, len(filesRaw) > 1)
					_ = os.MkdirAll(filepath.Dir(dst), 0o755)
					if err := os.WriteFile(dst, data, 0o644); err != nil {
						return err
					}
					results = append(results, map[string]any{
						"filename": name, "path": dst, "bytes": len(data),
					})
				}
				if len(results) == 1 {
					results[0]["url"] = args[0]
					results[0]["method"] = "scrapium"
					return out.WriteSuccess(os.Stdout, flags.pretty, "browser.download", results[0])
				}
				return out.WriteSuccess(os.Stdout, flags.pretty, "browser.download", map[string]any{
					"url": args[0], "files": results, "method": "scrapium",
				})

			case "fetch":
				// fetch() fallback: {b64, mime, status}.
				fetchData, _ := raw["fetch"].(map[string]any)
				b64, _ := fetchData["b64"].(string)
				mime, _ := fetchData["mime"].(string)
				status, _ := fetchData["status"].(float64)
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					return fmt.Errorf("decode fetch body: %w", err)
				}
				dst, err := resolveDownloadPath(flags, args[0])
				if err != nil {
					return err
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					return err
				}
				return out.WriteSuccess(os.Stdout, flags.pretty, "browser.download", map[string]any{
					"url": args[0], "path": dst, "bytes": len(data),
					"mime": mime, "status": int(status), "method": "fetch",
				})

			default:
				return fmt.Errorf("unexpected download response method: %q", method)
			}
		},
	}
	return cmd
}

func resolveBrowserDownloadPath(flags *rootFlags, filename string, multi bool) string {
	// -o takes precedence for single-file downloads (user named it explicitly).
	if flags.outputPath != "" && !multi {
		return flags.outputPath
	}
	// -O <dir>: save with the original filename into the target dir.
	if flags.outputDir != "" {
		return filepath.Join(flags.outputDir, filename)
	}
	// -o with multi-file: treat -o as a directory (better than clobbering).
	if flags.outputPath != "" && multi {
		_ = os.MkdirAll(flags.outputPath, 0o755)
		return filepath.Join(flags.outputPath, filename)
	}
	// No output flags: save to cwd with original filename.
	return filename
}

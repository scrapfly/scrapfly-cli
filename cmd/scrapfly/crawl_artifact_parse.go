package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

// newCrawlArtifactParseCmd adds `crawl artifact` sub-subcommands for
// inspecting WARC/HAR files downloaded via `crawl artifact <uuid> -o file`.
// Uses scrapfly-go-sdk's ParseHAR / ParseWARC so behaviour matches the SDK.
func newCrawlArtifactParseCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "parse",
		Short: "Inspect downloaded HAR / WARC artifacts (list urls, extract a response, …)",
	}
	cmd.AddCommand(newHarListCmd(flags))
	cmd.AddCommand(newHarGetCmd(flags))
	cmd.AddCommand(newWarcListCmd(flags))
	cmd.AddCommand(newWarcGetCmd(flags))
	return cmd
}

func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" || path == "" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func newHarListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "har-list [file|-]",
		Short: "List URLs + status codes from a HAR file (- for stdin)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "-"
			if len(args) == 1 {
				path = args[0]
			}
			data, err := readFileOrStdin(path)
			if err != nil {
				return err
			}
			h, err := scrapfly.ParseHAR(data)
			if err != nil {
				return err
			}
			type row struct {
				URL        string `json:"url"`
				Method     string `json:"method"`
				StatusCode int    `json:"status_code"`
				MIMEType   string `json:"content_type"`
				Size       int    `json:"bytes"`
			}
			var rows []row
			for _, e := range h.Entries() {
				rows = append(rows, row{
					URL: e.URL(), Method: e.Method(), StatusCode: e.StatusCode(),
					MIMEType: e.ContentType(), Size: e.ContentSize(),
				})
			}
			if flags.pretty {
				for _, r := range rows {
					fmt.Fprintf(os.Stdout, "%-3d %-6s %-40s %s\n", r.StatusCode, r.Method, r.MIMEType, r.URL)
				}
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.artifact.har.list", rows)
		},
	}
}

func newHarGetCmd(flags *rootFlags) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "har-get <file|-> <url>",
		Short: "Extract one response body from a HAR file by URL",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readFileOrStdin(args[0])
			if err != nil {
				return err
			}
			h, err := scrapfly.ParseHAR(data)
			if err != nil {
				return err
			}
			entry := h.FindByURL(args[1])
			if entry == nil {
				return fmt.Errorf("no entry matches URL %q", args[1])
			}
			body := entry.Content()
			// -o / -O writes the body; otherwise pick raw vs JSON envelope.
			if dst, err := resolveOutputPath(flags, args[1], mimeExt(entry.ContentType())); err == nil && dst != "" {
				if err := os.WriteFile(dst, body, 0o644); err != nil {
					return err
				}
				return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.artifact.har.get", map[string]any{
					"url": entry.URL(), "path": dst, "bytes": len(body), "content_type": entry.ContentType(),
				})
			}
			if raw {
				_, err := os.Stdout.Write(body)
				return err
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.artifact.har.get", map[string]any{
				"url":          entry.URL(),
				"status":       entry.StatusCode(),
				"content_type": entry.ContentType(),
				"bytes":        len(body),
				"body":         string(body),
			})
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "write body bytes to stdout (no JSON envelope)")
	return cmd
}

func newWarcListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "warc-list [file|-]",
		Short: "List URLs discovered in a WARC file (one URL per line)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "-"
			if len(args) == 1 {
				path = args[0]
			}
			data, err := readFileOrStdin(path)
			if err != nil {
				return err
			}
			w, err := scrapfly.ParseWARC(data)
			if err != nil {
				return err
			}
			pages, err := w.GetPages()
			if err != nil {
				return err
			}
			if flags.pretty {
				for _, p := range pages {
					fmt.Fprintln(os.Stdout, p.URL)
				}
				return nil
			}
			// Always emit a JSON envelope with the pages.
			type row struct {
				URL        string `json:"url"`
				StatusCode int    `json:"status_code,omitempty"`
				BodySize   int    `json:"body_size,omitempty"`
			}
			var rows []row
			for _, p := range pages {
				rows = append(rows, row{URL: p.URL, StatusCode: p.StatusCode, BodySize: len(p.Content)})
			}
			return out.WriteSuccess(os.Stdout, false, "crawl.artifact.warc.list", rows)
		},
	}
}

func newWarcGetCmd(flags *rootFlags) *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "warc-get <file|-> <url>",
		Short: "Extract one page body from a WARC file by URL",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readFileOrStdin(args[0])
			if err != nil {
				return err
			}
			w, err := scrapfly.ParseWARC(data)
			if err != nil {
				return err
			}
			pages, err := w.GetPages()
			if err != nil {
				return err
			}
			for _, p := range pages {
				if p.URL == args[1] {
					if dst, err := resolveOutputPath(flags, args[1], ""); err == nil && dst != "" {
						if err := os.WriteFile(dst, p.Content, 0o644); err != nil {
							return err
						}
						return out.WriteSuccess(os.Stdout, flags.pretty, "crawl.artifact.warc.get", map[string]any{
							"url": p.URL, "path": dst, "bytes": len(p.Content), "status": p.StatusCode,
						})
					}
					if raw {
						_, err := os.Stdout.Write(p.Content)
						return err
					}
					// Fall back to JSON envelope.
					return json.NewEncoder(os.Stdout).Encode(map[string]any{
						"success": true, "product": "crawl.artifact.warc.get",
						"data": map[string]any{
							"url": p.URL, "status": p.StatusCode, "bytes": len(p.Content), "body": string(p.Content),
						},
					})
				}
			}
			return fmt.Errorf("no page matches URL %q", args[1])
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "write body bytes to stdout (no JSON envelope)")
	return cmd
}

func mimeExt(mime string) string {
	switch {
	case mime == "":
		return "bin"
	case mime == "text/html" || mime == "text/html; charset=utf-8":
		return "html"
	case mime == "application/json":
		return "json"
	case mime == "text/css":
		return "css"
	case mime == "text/javascript" || mime == "application/javascript":
		return "js"
	case mime == "image/png":
		return "png"
	case mime == "image/jpeg":
		return "jpg"
	}
	return "bin"
}

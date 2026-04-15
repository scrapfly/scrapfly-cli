package main

import (
	"encoding/base64"
	"fmt"
	"os"

	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"
)

func newScreenshotCmd(flags *rootFlags) *cobra.Command {
	var (
		format           string
		capture          string
		resolution       string
		country          string
		waitForSelector  string
		renderingWait    int
		autoScroll       bool
		options          []string
		cache            bool
		cacheTTL         int
		cacheClear       bool
		jsFile           string
		timeoutMs        int
		webhook          string
		visionDeficiency string
	)

	cmd := &cobra.Command{
		Use:   "screenshot <url>",
		Short: "Capture a screenshot via Scrapfly Screenshot API",
		Long: `Capture a screenshot of a URL. Output is binary (jpg|png|webp|gif):
specify -o <file> to write to an exact path, or -O <dir> to auto-name.
Without either, the image is returned base64-encoded inside the JSON envelope.`,
		Example: `  # Save to an explicit path
  scrapfly -o shot.png screenshot https://example.com --format png --resolution 1920x1080

  # Save into a directory (auto-named as <host>-<path>-<timestamp>.<ext>)
  scrapfly -O ./shots screenshot https://web-scraping.dev/products

  # Capture a specific element, block cookie banners
  scrapfly -o hero.png screenshot https://example.com \
    --capture "main .hero" --option block_banners --option dark_mode`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			applyProductDefaults(cmd, map[string]*string{
				"country": &country,
			}, map[string]*bool{
				"cache": &cache,
			})
			cfg := &scrapfly.ScreenshotConfig{
				URL:                  args[0],
				Format:               scrapfly.ScreenshotFormat(format),
				Capture:              capture,
				Resolution:           resolution,
				Country:              country,
				WaitForSelector:      waitForSelector,
				RenderingWait:        renderingWait,
				AutoScroll:           autoScroll,
				Cache:                cache,
				CacheTTL:             cacheTTL,
				CacheClear:           cacheClear,
				Webhook:              webhook,
				VisionDeficiencyType: scrapfly.VisionDeficiencyType(visionDeficiency),
				Timeout:              timeoutMs,
			}
			for _, o := range options {
				cfg.Options = append(cfg.Options, scrapfly.ScreenshotOption(o))
			}
			if jsFile != "" {
				raw, err := os.ReadFile(jsFile)
				if err != nil {
					return fmt.Errorf("read --js-file: %w", err)
				}
				cfg.JS = string(raw)
			}

			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			result, err := client.Screenshot(cfg)
			if err != nil {
				return err
			}

			dst, err := resolveOutputPath(flags, args[0], result.Metadata.ExtensionName)
			if err != nil {
				return err
			}
			if dst != "" {
				if err := os.WriteFile(dst, result.Image, 0o644); err != nil {
					return err
				}
				if flags.pretty {
					out.Pretty(os.Stdout, "saved %s (%d bytes, %s, upstream=%d)",
						dst, len(result.Image), result.Metadata.ExtensionName, result.Metadata.UpstreamStatusCode)
					return nil
				}
				return out.WriteSuccess(os.Stdout, false, "screenshot", map[string]any{
					"path":                 dst,
					"bytes":                len(result.Image),
					"extension":            result.Metadata.ExtensionName,
					"upstream_status_code": result.Metadata.UpstreamStatusCode,
					"upstream_url":         result.Metadata.UpstreamURL,
				})
			}

			if flags.pretty {
				return fmt.Errorf("screenshot in --pretty mode requires -o/--output or -O/--output-dir (binary payload)")
			}
			return out.WriteSuccess(os.Stdout, false, "screenshot", map[string]any{
				"image_base64":         base64.StdEncoding.EncodeToString(result.Image),
				"bytes":                len(result.Image),
				"extension":            result.Metadata.ExtensionName,
				"upstream_status_code": result.Metadata.UpstreamStatusCode,
				"upstream_url":         result.Metadata.UpstreamURL,
			})
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "jpg|png|webp|gif")
	cmd.Flags().StringVar(&capture, "capture", "", "fullpage or CSS selector")
	cmd.Flags().StringVar(&resolution, "resolution", "", "viewport e.g. 1920x1080")
	cmd.Flags().StringVar(&country, "country", "", "proxy country")
	cmd.Flags().StringVar(&waitForSelector, "wait-for-selector", "", "CSS selector to wait for")
	cmd.Flags().IntVar(&renderingWait, "rendering-wait", 0, "extra wait ms after load")
	cmd.Flags().BoolVar(&autoScroll, "auto-scroll", false, "auto-scroll page")
	cmd.Flags().StringSliceVar(&options, "option", nil, "screenshot option (load_images|dark_mode|block_banners|print_media_format, repeatable)")
	cmd.Flags().BoolVar(&cache, "cache", false, "enable caching")
	cmd.Flags().IntVar(&cacheTTL, "cache-ttl", 0, "cache TTL seconds")
	cmd.Flags().BoolVar(&cacheClear, "cache-clear", false, "force cache refresh for this request")
	cmd.Flags().StringVar(&jsFile, "js-file", "", "JS to execute before capture")
	cmd.Flags().IntVar(&timeoutMs, "request-timeout", 0, "upstream timeout ms")
	cmd.Flags().StringVar(&webhook, "webhook", "", "webhook name to notify on completion")
	cmd.Flags().StringVar(&visionDeficiency, "vision-deficiency", "",
		"simulate vision deficiency: deuteranopia|protanopia|tritanopia|achromatopsia|blurredVision|reducedContrast")

	return cmd
}

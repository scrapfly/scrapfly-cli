package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	scrapfly "github.com/scrapfly/go-scrapfly"
)

var errMissingAPIKey = errors.New("missing API key: set SCRAPFLY_API_KEY or pass --api-key")

func buildClient(flags *rootFlags) (*scrapfly.Client, error) {
	key := flags.apiKey
	if key == "" {
		key = os.Getenv("SCRAPFLY_API_KEY")
	}
	host := flags.host
	if host == "" {
		host = os.Getenv("SCRAPFLY_API_HOST")
	}
	if key == "" || host == "" {
		if cfg, err := loadConfig(); err == nil {
			if key == "" {
				key = cfg.APIKey
			}
			if host == "" {
				host = cfg.Host
			}
		}
	}
	if key == "" {
		return nil, errMissingAPIKey
	}

	var (
		client *scrapfly.Client
		err    error
	)
	if host != "" || flags.insecure {
		if host == "" {
			host = "https://api.scrapfly.io"
		}
		client, err = scrapfly.NewWithHost(key, host, !flags.insecure)
	} else {
		client, err = scrapfly.New(key)
	}
	if err != nil {
		return nil, err
	}

	httpClient := client.HTTPClient()
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpClient.Timeout = flags.timeout
	if flags.insecure {
		tr, ok := httpClient.Transport.(*http.Transport)
		if !ok || tr == nil {
			tr = http.DefaultTransport.(*http.Transport).Clone()
		}
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		httpClient.Transport = tr
	}
	client.SetHTTPClient(httpClient)

	cbHost := flags.browserHost
	if cbHost == "" {
		cbHost = os.Getenv("SCRAPFLY_BROWSER_HOST")
	}
	if cbHost == "" {
		if cfg, err := loadConfig(); err == nil && cfg != nil {
			// Future: add cloud_browser_host to configFile if needed.
			_ = cfg
		}
	}
	if cbHost != "" {
		client.SetCloudBrowserHost(cbHost)
	}

	return client, nil
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// resolveOutputPath returns the final file path to write a binary payload to.
//
//   - If --output is set, it wins (returned as-is).
//   - If --output-dir is set, the dir is created and a filename is generated
//     from `hint` (typically a URL) plus a timestamp and `ext`.
//   - Returns "" if neither flag is set.
func resolveOutputPath(flags *rootFlags, hint, ext string) (string, error) {
	if flags.outputPath != "" && flags.outputDir != "" {
		return "", fmt.Errorf("--output and --output-dir are mutually exclusive")
	}
	if flags.outputPath != "" {
		return flags.outputPath, nil
	}
	if flags.outputDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(flags.outputDir, 0o755); err != nil {
		return "", fmt.Errorf("create --output-dir: %w", err)
	}
	name := slugifyHint(hint)
	if name == "" {
		name = "scrapfly"
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" {
		ext = "bin"
	}
	return filepath.Join(flags.outputDir, fmt.Sprintf("%s-%s.%s", name, stamp, ext)), nil
}

func slugifyHint(hint string) string {
	if hint == "" {
		return ""
	}
	if u, err := url.Parse(hint); err == nil && u.Host != "" {
		path := strings.Trim(u.Path, "/")
		if path == "" {
			return slugRe.ReplaceAllString(u.Host, "-")
		}
		return slugRe.ReplaceAllString(u.Host+"-"+path, "-")
	}
	return slugRe.ReplaceAllString(hint, "-")
}

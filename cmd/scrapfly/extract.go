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

func newExtractCmd(flags *rootFlags) *cobra.Command {
	var (
		file         string
		contentType  string
		url          string
		charset      string
		prompt       string
		model        string
		template     string
		templateFile string
		webhook      string
		compression  string
		timeoutSec   int
		dataOnly     bool
	)

	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract structured data from a document via the Extraction API",
		Long: `Run AI/template extraction on a document you already have. The document body
is read from --file or stdin. --content-type is required (e.g. text/html).

Pick exactly one extraction mode:
  --prompt    free-form AI instruction
  --model     named model (product, article, job_posting, hotel, review_list, ...)
  --template  saved template name

For end-to-end "fetch + extract" use ` + "`scrapfly scrape --extraction-prompt ...`" + `.`,
		Example: `  # Pipe from scrape (fetch with Scrapfly, then extract)
  scrapfly scrape https://web-scraping.dev/product/1 --render-js --proxified \
    | scrapfly extract --content-type text/html \
        --url https://web-scraping.dev/product/1 \
        --prompt "product name, price, sku, description"

  # Prompt-based extraction from stdin (raw curl)
  curl -s https://example.com/product \
    | scrapfly extract --content-type text/html --prompt "product name, price, sku"

  # Model-based extraction from a local file
  scrapfly extract --file page.html --content-type text/html --model product

  # Pass URL for extraction context
  scrapfly extract --file page.html --content-type text/html \
    --url https://example.com/p/42 --prompt "shipping info"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if contentType == "" {
				return fmt.Errorf("--content-type is required (e.g. text/html)")
			}

			var (
				body []byte
				err  error
			)
			if file != "" {
				body, err = os.ReadFile(file)
				if err != nil {
					return fmt.Errorf("read --file: %w", err)
				}
			} else {
				body, err = io.ReadAll(os.Stdin)
				if err != nil {
					return fmt.Errorf("read stdin: %w", err)
				}
			}
			if len(body) == 0 {
				return fmt.Errorf("empty body (provide --file or pipe content via stdin)")
			}

			cfg := &scrapfly.ExtractionConfig{
				Body:                      body,
				ContentType:               contentType,
				URL:                       url,
				Charset:                   charset,
				ExtractionPrompt:          prompt,
				ExtractionModel:           scrapfly.ExtractionModel(model),
				ExtractionTemplate:        template,
				Webhook:                   webhook,
				DocumentCompressionFormat: scrapfly.CompressionFormat(compression),
				Timeout:                   timeoutSec,
			}
			if compression != "" {
				cfg.IsDocumentCompressed = true
			}
			if templateFile != "" {
				raw, err := os.ReadFile(templateFile)
				if err != nil {
					return fmt.Errorf("read --template-file: %w", err)
				}
				var m map[string]interface{}
				if err := json.Unmarshal(raw, &m); err != nil {
					return fmt.Errorf("parse --template-file (JSON object expected): %w", err)
				}
				cfg.ExtractionEphemeralTemplate = m
			}

			client, err := buildClient(flags)
			if err != nil {
				return err
			}
			result, err := client.Extract(cfg)
			if err != nil {
				return err
			}
			if dataOnly {
				// result.Data may be a string or a structured object. For
				// strings, write as-is; for objects, marshal to JSON (one
				// line). This keeps pipes clean for both cases.
				switch v := result.Data.(type) {
				case string:
					_, err := os.Stdout.WriteString(v)
					return err
				default:
					b, err := json.Marshal(v)
					if err != nil {
						return err
					}
					_, err = os.Stdout.Write(append(b, '\n'))
					return err
				}
			}
			if flags.pretty {
				out.Pretty(os.Stdout, "extracted content_type=%s", result.ContentType)
				return nil
			}
			return out.WriteSuccess(os.Stdout, false, "extract", result)
		},
	}

	cmd.Flags().StringVar(&file, "file", "", "read document body from file (default: stdin)")
	cmd.Flags().StringVar(&contentType, "content-type", "", "document content type, e.g. text/html (required)")
	cmd.Flags().StringVar(&url, "url", "", "source URL of the document (for context)")
	cmd.Flags().StringVar(&charset, "charset", "", "document charset")
	cmd.Flags().StringVar(&prompt, "prompt", "", "AI extraction prompt")
	cmd.Flags().StringVar(&model, "model", "", "extraction model (product, article, ...)")
	cmd.Flags().StringVar(&template, "template", "", "saved extraction template name")
	cmd.Flags().StringVar(&templateFile, "template-file", "", "JSON file with an ephemeral extraction template (inline rules)")
	cmd.Flags().StringVar(&webhook, "webhook", "", "webhook name to notify on completion")
	cmd.Flags().StringVar(&compression, "compression", "", "document compression (gzip|zstd|deflate) if pre-compressed")
	cmd.Flags().IntVar(&timeoutSec, "request-timeout", 0, "extraction timeout (seconds)")
	cmd.Flags().BoolVar(&dataOnly, "data-only", false, "print the extracted data to stdout with no JSON envelope (one line if object, raw if string)")

	return cmd
}

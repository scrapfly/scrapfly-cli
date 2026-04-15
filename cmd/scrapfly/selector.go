package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	scrapfly "github.com/scrapfly/go-scrapfly"
	"github.com/scrapfly/scrapfly-cli/internal/llm"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/spf13/cobra"

	// Register LLM providers via side-effect imports.
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/anthropic"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/gemini"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/ollama"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/openai"
)

// newSelectorCmd: offline CSS-selector builder. Give it a saved HTML and a
// natural-language description; it asks the LLM for a selector, verifies the
// selector against the DOM (match count, sample text), and retries if the
// first try is ambiguous or missing.
//
// Handy for building long-lived scrapers where you want a stable selector
// locked in without running a browser session.
func newSelectorCmd(flags *rootFlags) *cobra.Command {
	var (
		file        string
		fromURL     string
		provider    string
		model       string
		attempts    int
		saveHTML    string
		wantText    string
		includeText bool
	)
	cmd := &cobra.Command{
		Use:   "selector <description>",
		Short: "Find a robust CSS selector for an element described in natural language",
		Long: `Workflow:
  1. Load HTML from --file, --url (scrapes via Scrapfly), or stdin.
  2. Ask the LLM for a CSS selector that matches the description.
  3. Verify with goquery: the selector must match at least one element and
     ideally exactly one. The matched element's text is included in the
     result so you can eyeball that it's the right thing.
  4. If the candidate isn't unique or matches nothing, retry (up to
     --attempts) with the failure fed back to the model.

Pairs well with --want-text "<expected substring>" which adds an automated
double-check: the matched element's inner text must contain that substring
for the run to succeed.`,
		Example: `  # From a saved file
  scrapfly selector "the first product price" --file page.html

  # Scrape + find in one shot, save the HTML for reuse
  scrapfly selector "login submit button" --url https://web-scraping.dev/login \
    --save-html login.html --want-text Submit

  # Pipe HTML in, use Gemini
  cat page.html | scrapfly selector "cookie banner dismiss" --provider gemini`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			description := args[0]
			html, err := loadHTMLForSelector(flags, file, fromURL)
			if err != nil {
				return err
			}
			if saveHTML != "" {
				if err := os.WriteFile(saveHTML, []byte(html), 0o644); err != nil {
					return fmt.Errorf("save --save-html: %w", err)
				}
			}
			result, err := runSelectorFinder(cmd.Context(), flags, description, html, wantText, attempts, provider, model)
			if err != nil {
				return err
			}
			if !includeText {
				delete(result, "sample_text")
			}
			return out.WriteSuccess(os.Stdout, flags.pretty, "selector", result)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "HTML input file (`-` for stdin)")
	cmd.Flags().StringVar(&fromURL, "url", "", "scrape this URL first (uses the current Scrapfly credentials)")
	cmd.Flags().StringVar(&saveHTML, "save-html", "", "write the HTML used for selector-finding to this path (useful with --url)")
	cmd.Flags().StringVar(&wantText, "want-text", "", "reject any selector whose first match doesn't contain this substring (case-insensitive)")
	cmd.Flags().BoolVar(&includeText, "include-text", true, "include the first match's text in the result envelope")
	cmd.Flags().IntVar(&attempts, "attempts", 4, "max LLM re-asks when the first selector isn't unique / matches nothing")
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|gemini|ollama (default auto-detect)")
	cmd.Flags().StringVar(&model, "model", "", "override model id")
	return cmd
}

// runSelectorFinder is the core selector discovery loop extracted so both
// the `selector` CLI command and the MCP tool can call it.
func runSelectorFinder(ctx context.Context, flags *rootFlags, description, html, wantText string, attempts int, provider, model string) (map[string]any, error) {
	if attempts <= 0 {
		attempts = 4
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	llmProv, err := llm.Autodetect(llm.Options{Provider: provider, Model: model})
	if err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}
	snapshot := snapshotHTMLForLLM(doc)

	var lastErr error
	var selector string
	var match *goquery.Selection
	history := []string{}
	for attempt := 1; attempt <= attempts; attempt++ {
		sel, err := askSelector(ctx, llmProv, description, snapshot, history)
		if err != nil {
			lastErr = err
			history = append(history, fmt.Sprintf("attempt %d: llm error: %v", attempt, err))
			continue
		}
		sel = strings.TrimSpace(sel)
		if sel == "" {
			lastErr = fmt.Errorf("llm returned empty selector")
			history = append(history, fmt.Sprintf("attempt %d: empty selector", attempt))
			continue
		}
		var m *goquery.Selection
		func() {
			defer func() {
				if r := recover(); r != nil {
					lastErr = fmt.Errorf("invalid selector %q: %v", sel, r)
					history = append(history, fmt.Sprintf("attempt %d: invalid selector %q", attempt, sel))
				}
			}()
			m = doc.Find(sel)
		}()
		if lastErr != nil && m == nil {
			continue
		}
		count := m.Length()
		if count == 0 {
			lastErr = fmt.Errorf("selector %q matched 0 elements", sel)
			history = append(history, fmt.Sprintf("attempt %d: %q matched nothing", attempt, sel))
			continue
		}
		if wantText != "" {
			firstText := strings.TrimSpace(m.First().Text())
			if !strings.Contains(strings.ToLower(firstText), strings.ToLower(wantText)) {
				lastErr = fmt.Errorf("selector %q matched %d but text missing %q", sel, count, wantText)
				history = append(history, fmt.Sprintf("attempt %d: %q text %q missing %q", attempt, sel, truncateStr(firstText, 80), wantText))
				continue
			}
		}
		selector = sel
		match = m
		lastErr = nil
		break
	}
	if selector == "" {
		return nil, fmt.Errorf("no robust selector after %d attempts: %w", attempts, lastErr)
	}
	return map[string]any{
		"description": description,
		"selector":    selector,
		"match_count": match.Length(),
		"sample_html": truncateStr(goqueryOuterHTML(match.First()), 500),
		"sample_text": truncateStr(strings.TrimSpace(match.First().Text()), 500),
	}, nil
}

func loadHTMLForSelector(flags *rootFlags, file, url string) (string, error) {
	switch {
	case url != "":
		client, err := buildClient(flags)
		if err != nil {
			return "", err
		}
		res, err := client.Scrape(&scrapfly.ScrapeConfig{URL: url, RenderJS: true})
		if err != nil {
			return "", fmt.Errorf("scrape --url: %w", err)
		}
		return res.Result.Content, nil
	case file == "-" || (file == "" && !stdinIsTTY()):
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("need HTML input: --file PATH, --url URL, or piped stdin")
	}
}

func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return true
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// snapshotHTMLForLLM produces a compact, token-efficient view of the DOM.
// Keeps interactive tags + heading/text-bearing tags with a handful of key
// attributes. Skips <script>/<style>/<svg>/comments.
func snapshotHTMLForLLM(doc *goquery.Document) string {
	keep := map[string]bool{
		"a": true, "button": true, "input": true, "select": true, "textarea": true,
		"label": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
		"h5": true, "h6": true, "nav": true, "main": true, "article": true,
		"section": true, "li": true, "td": true, "th": true, "img": true,
		"span": true, "div": true, "p": true,
	}
	var out strings.Builder
	doc.Find("*").Each(func(_ int, s *goquery.Selection) {
		node := goquery.NodeName(s)
		if !keep[node] {
			return
		}
		// For container-y tags, only keep when they have an id or class or aria.
		if node == "div" || node == "span" || node == "section" || node == "article" {
			if _, ok := s.Attr("id"); !ok {
				if _, ok := s.Attr("class"); !ok {
					if _, ok := s.Attr("aria-label"); !ok {
						return
					}
				}
			}
		}
		out.WriteString("<")
		out.WriteString(node)
		for _, a := range []string{"id", "name", "type", "role", "aria-label", "aria-labelledby", "placeholder", "data-testid", "class", "href", "for", "value"} {
			if v, ok := s.Attr(a); ok && v != "" {
				if a == "class" {
					v = truncateStr(v, 120)
				}
				out.WriteString(fmt.Sprintf(" %s=%q", a, v))
			}
		}
		text := strings.TrimSpace(s.Contents().Not("script,style").FilterFunction(func(_ int, c *goquery.Selection) bool {
			return goquery.NodeName(c) == "#text"
		}).Text())
		if text != "" {
			out.WriteString(">")
			out.WriteString(truncateStr(text, 120))
			out.WriteString("</")
			out.WriteString(node)
			out.WriteString(">")
		} else {
			out.WriteString("/>")
		}
		out.WriteString("\n")
	})
	// Cap to keep prompt bounded even on huge pages.
	s := out.String()
	if len(s) > 60000 {
		s = s[:60000] + "\n/* truncated */"
	}
	return s
}

func askSelector(parent context.Context, provider llm.Provider, description, snapshot string, history []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	system := `You produce a single CSS selector (cascadia-compatible) that uniquely
identifies the HTML element described in natural language.

Rules:
- Output EXACTLY ONE CSS selector, nothing else. No backticks, no prose,
  no explanation, no JSON.
- Prefer stable anchors: unique id, data-testid, [name=...], aria-label,
  specific text with :has + :contains (cascadia supports :contains).
- Avoid positional numeric :nth-child unless required.
- Avoid matching more than one element.

Previous attempts (for context, do not repeat):
`
	if len(history) > 0 {
		system += "  - " + strings.Join(history, "\n  - ")
	} else {
		system += "  (none)"
	}

	user := fmt.Sprintf("Target description:\n  %s\n\nCompact DOM:\n%s", description, snapshot)

	resp, err := provider.Chat(ctx, llm.ChatRequest{
		System:    system,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: user}},
		MaxTokens: 512,
	})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Text)
	// Strip common wrappers: ```css ... ``` or backticks.
	text = strings.TrimPrefix(text, "```css")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	// Pick the first non-empty line.
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.Trim(line, "`\"' ")
		if line != "" {
			return line, nil
		}
	}
	return "", fmt.Errorf("empty response")
}

func goqueryOuterHTML(s *goquery.Selection) string {
	if s.Length() == 0 {
		return ""
	}
	h, err := goquery.OuterHtml(s)
	if err != nil {
		return ""
	}
	// collapse whitespace for readability
	return regexp.MustCompile(`\s+`).ReplaceAllString(h, " ")
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

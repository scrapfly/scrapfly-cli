package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/scrapfly/scrapfly-cli/internal/llm"
	"github.com/scrapfly/scrapfly-cli/internal/out"
	"github.com/scrapfly/scrapfly-cli/internal/sessiond"
	"github.com/spf13/cobra"

	// Register LLM providers via side-effect imports.
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/anthropic"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/gemini"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/ollama"
	_ "github.com/scrapfly/scrapfly-cli/internal/llm/openai"
)

// resolveRefAI asks the default LLM provider for the single best-matching
// AXTree ref given a natural-language description and the current snapshot
// from the session daemon. Keeps the prompt tiny (~1-2k tokens).
func resolveRefAI(description, providerName, model string) (string, error) {
	sid, ok := sessiond.Resolve(sessionIDFlag)
	if !ok {
		return "", fmt.Errorf("no active session")
	}
	snap, err := sessiond.Send(sid, sessiond.Request{Action: "snapshot"})
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	var payload struct {
		Nodes []struct {
			Ref         string `json:"ref"`
			Role        string `json:"role"`
			Name        string `json:"name,omitempty"`
			Value       string `json:"value,omitempty"`
			Description string `json:"description,omitempty"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(snap.Data, &payload); err != nil {
		return "", err
	}
	// Shrink the tree to actionable elements with an accessible name. Anything
	// without a name is useless for NL resolution and just wastes tokens
	// (also confuses the model for terse providers like Gemini flash).
	actionableRoles := map[string]bool{
		"button": true, "textbox": true, "combobox": true, "searchbox": true,
		"link": true, "checkbox": true, "radio": true, "switch": true,
		"menuitem": true, "tab": true, "option": true, "slider": true,
		"spinbutton": true,
	}
	var filtered []map[string]any
	for _, n := range payload.Nodes {
		if !actionableRoles[n.Role] {
			continue
		}
		if n.Name == "" && n.Value == "" {
			continue
		}
		entry := map[string]any{"ref": n.Ref, "role": n.Role}
		if n.Name != "" {
			entry["name"] = n.Name
		}
		if n.Value != "" {
			entry["value"] = n.Value
		}
		if n.Description != "" {
			entry["description"] = n.Description
		}
		filtered = append(filtered, entry)
	}
	if len(filtered) == 0 {
		return "", fmt.Errorf("snapshot has no actionable elements (try taking a snapshot first)")
	}
	tree, _ := json.Marshal(filtered)

	provider, err := llm.Autodetect(llm.Options{Provider: providerName, Model: model})
	if err != nil {
		return "", fmt.Errorf("llm: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	system := `You match natural-language descriptions to accessibility tree nodes.

Input: a JSON array of nodes {ref, role, name, value?, description?} and a
description string.

Output: exactly one token, the ref of the best match (shape: e followed by
one or more digits, e.g. e12, e345). If and only if nothing matches, output
the literal token NONE. Never output partial refs like "e", never wrap in
quotes, never add prose. No markdown.

Examples of CORRECT output: e3 | e22 | e107 | NONE
Examples of WRONG output:   "e22" | e | ref: e22 | The answer is e22.`

	user := fmt.Sprintf("Description: %s\n\nNodes:\n%s\n\nAnswer:", description, string(tree))

	// Gemini 2.5 reserves thinking tokens inside MaxTokens; keep plenty of
	// headroom so the final ref token still fits.
	resp, err := provider.Chat(ctx, llm.ChatRequest{
		System:    system,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: user}},
		MaxTokens: 1024,
	})
	if err != nil {
		return "", err
	}
	text := resp.Text
	if strings.Contains(text, "NONE") {
		return "", fmt.Errorf("no element matches %q", description)
	}
	// Extract the first e<digits> token regardless of surrounding prose or
	// markdown (some providers like Gemini occasionally wrap the answer).
	m := regexp.MustCompile(`\be(\d+)\b`).FindString(text)
	if m == "" {
		return "", fmt.Errorf("could not parse a ref from LLM output %q", strings.TrimSpace(text))
	}
	valid := false
	for _, n := range payload.Nodes {
		if n.Ref == m {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("LLM returned unknown ref %q for %q", m, description)
	}
	return m, nil
}

// maybeResolveAI inspects a locator: if it starts with "ai:", the remainder
// is sent to the LLM to pick a ref from the current AXTree snapshot, and
// the resolved ref is returned. Otherwise the locator is passed through.
//
// Used by click / fill / scroll so callers can write things like
// `scrapfly browser click 'ai:the sign-in button'` anywhere a locator is
// accepted.
func maybeResolveAI(locator string) (string, error) {
	if !strings.HasPrefix(locator, "ai:") {
		return locator, nil
	}
	desc := strings.TrimSpace(strings.TrimPrefix(locator, "ai:"))
	if desc == "" {
		return "", fmt.Errorf("ai: prefix requires a description (e.g. ai:\"username\")")
	}
	ref, err := resolveRefAI(desc, "", "")
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "[ai] %q → %s\n", desc, ref)
	return ref, nil
}

func newBrowserFillAICmd(flags *rootFlags) *cobra.Command {
	var provider, model string
	cmd := &cobra.Command{
		Use:   "fill-ai <description> <value>",
		Short: "Fill an element by natural-language description (LLM resolves the ref)",
		Long: `Takes a snapshot, asks the configured LLM which ref best matches the description,
then fills that ref. Saves you from hand-crafting selectors for ambiguous pages.

Provider auto-detects from ANTHROPIC_API_KEY / OPENAI_API_KEY / GEMINI_API_KEY /
OLLAMA_HOST. Override with --provider / --model.`,
		Example: `  scrapfly browser fill-ai "username" user123
  scrapfly browser fill-ai "the email field" me@example.com --provider openai`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveRefAI(args[0], provider, model)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[fill-ai] %q → %s\n", args[0], ref)
			sid, _ := sessiond.Resolve(sessionIDFlag)
			resp, err := sessiond.Send(sid, sessiond.Request{Action: "fill", Ref: ref, Text: args[1]})
			if err != nil {
				return err
			}
			var data any
			_ = json.Unmarshal(resp.Data, &data)
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.fill-ai", map[string]any{
				"description": args[0], "ref": ref, "result": data,
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|gemini|ollama (default: auto-detect)")
	cmd.Flags().StringVar(&model, "model", "", "override model id")
	return cmd
}

func newBrowserClickAICmd(flags *rootFlags) *cobra.Command {
	var provider, model string
	cmd := &cobra.Command{
		Use:     "click-ai <description>",
		Short:   "Click an element by natural-language description (LLM resolves the ref)",
		Example: `  scrapfly browser click-ai "sign in button"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := resolveRefAI(args[0], provider, model)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "[click-ai] %q → %s\n", args[0], ref)
			sid, _ := sessiond.Resolve(sessionIDFlag)
			resp, err := sessiond.Send(sid, sessiond.Request{Action: "click", Ref: ref})
			if err != nil {
				return err
			}
			var data any
			_ = json.Unmarshal(resp.Data, &data)
			return out.WriteSuccess(os.Stdout, flags.pretty, "browser.click-ai", map[string]any{
				"description": args[0], "ref": ref, "result": data,
			})
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "anthropic|openai|gemini|ollama (default: auto-detect)")
	cmd.Flags().StringVar(&model, "model", "", "override model id")
	return cmd
}

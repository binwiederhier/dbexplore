package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const assistSystem = `You write PostgreSQL queries for a read-only database explorer.
Reply with ONLY one SQL statement, no prose, no markdown fences. It must be a single read-only statement (SELECT, WITH or EXPLAIN).
Only use tables and columns from the schema below. Prefer explicit column lists for wide tables, and add ORDER BY where it helps.
Do not add LIMIT unless the user asks for a specific number; the explorer pages results itself.
`

// assistant turns a natural-language request into one SQL statement with Claude.
type assistant struct {
	client  anthropic.Client
	model   string
	context string
}

func newAssistant(c *aiConfig) *assistant {
	if c == nil {
		return nil
	}
	key := c.APIKey
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return nil
	}
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	return &assistant{
		client:  anthropic.NewClient(opts...),
		model:   c.Model,
		context: c.Context,
	}
}

var sqlFence = regexp.MustCompile("(?i)^```(?:sql)?\\s*|\\s*```$")

func (a *assistant) writeSQL(ctx context.Context, schema string, req assistRequest) (string, error) {
	system := assistSystem
	if a.context != "" {
		system += "\nAbout this database: " + a.context + "\n"
	}
	system += "\nSchema (table(column type, ...)):\n" + schema
	msg := strings.TrimSpace(req.Prompt)
	if sql := strings.TrimSpace(req.CurrentSQL); sql != "" {
		msg += "\n\nCurrent query in the editor (modify it if the request refers to it):\n" + sql
	}
	if req.Table != "" {
		msg += "\n\nThe user currently has table " + req.Table + " open."
	}
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 16000,
		System: []anthropic.TextBlockParam{{
			Text:         system,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(msg))},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", userErrorf("AI request failed (HTTP %d): %s", apiErr.StatusCode, apiErr.Error())
		}
		return "", userErrorf("AI request failed: %v", err)
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", userErrorf("the model declined this request")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(sqlFence.ReplaceAllString(strings.TrimSpace(text.String()), "")), nil
}

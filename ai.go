package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	anthropicURL     = "https://api.anthropic.com"
	anthropicVersion = "2023-06-01"
	anthropicTokens  = 16000

	// hostit runs a container API on loopback in every app container. Its assistant endpoint
	// answers prompts with the operator's Claude backend, so apps need no key of their own.
	hostitURL    = "http://127.0.0.1:2586"
	hostitTokens = 4096 // hostit clamps to 8192

	aiTimeout    = 5 * time.Minute
	probeTimeout = time.Second
)

const assistSystem = `You write PostgreSQL queries for a read-only database explorer.
Reply with ONLY one SQL statement, no prose, no markdown fences. It must be a single read-only statement (SELECT, WITH or EXPLAIN).
Only use tables and columns from the schema below. Prefer explicit column lists for wide tables, and add ORDER BY where it helps.
Do not add LIMIT unless the user asks for a specific number; the explorer pages results itself.
`

var sqlFence = regexp.MustCompile("(?i)^```(?:sql)?\\s*|\\s*```$")

// assistant turns a natural-language request into one SQL statement with Claude.
type assistant struct {
	backend aiBackend
	context string
}

// aiBackend sends one system prompt plus user message and returns the reply text.
type aiBackend interface {
	complete(ctx context.Context, system, prompt string) (string, error)
	name() string
}

// newAssistant picks the Anthropic API if there is a key (ai.api_key or $ANTHROPIC_API_KEY),
// else hostit's container API if dbexplore runs as a hostit app. Without either it returns nil
// and Ask AI is hidden.
func newAssistant(c *aiConfig) *assistant {
	return newAssistantWith(c, hostitURL)
}

func newAssistantWith(c *aiConfig, hostitURL string) *assistant {
	if c == nil {
		c = &aiConfig{}
	}
	key := c.APIKey
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	client := &http.Client{Timeout: aiTimeout}
	var backend aiBackend
	switch {
	case key != "":
		url, model := c.BaseURL, c.Model
		if url == "" {
			url = anthropicURL
		}
		if model == "" {
			model = defaultModel
		}
		backend = &anthropicBackend{url: strings.TrimSuffix(url, "/"), key: key, model: model, http: client}
	case hostitAvailable(hostitURL):
		backend = &hostitBackend{url: hostitURL, model: c.Model, http: client}
	default:
		return nil
	}
	return &assistant{backend: backend, context: c.Context}
}

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
	text, err := a.backend.complete(ctx, system, msg)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sqlFence.ReplaceAllString(strings.TrimSpace(text), "")), nil
}

// anthropicBackend calls the Messages API directly, with adaptive thinking and the system prompt
// (instructions plus schema, the same for every request against a source) marked for caching.
type anthropicBackend struct {
	url   string
	key   string
	model string
	http  *http.Client
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (b *anthropicBackend) name() string {
	return "anthropic"
}

func (b *anthropicBackend) complete(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{
		"model":      b.model,
		"max_tokens": anthropicTokens,
		"system":     []map[string]any{{"type": "text", "text": system, "cache_control": map[string]string{"type": "ephemeral"}}},
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
		"thinking":   map[string]string{"type": "adaptive"},
	}
	var resp anthropicResponse
	status, err := postJSON(ctx, b.http, b.url+"/v1/messages", map[string]string{"x-api-key": b.key, "anthropic-version": anthropicVersion}, body, &resp)
	if err != nil {
		return "", userErrorf("AI request failed: %v", err)
	}
	if status != http.StatusOK {
		msg := http.StatusText(status)
		if resp.Error != nil && resp.Error.Message != "" {
			msg = resp.Error.Message
		}
		return "", userErrorf("AI request failed (HTTP %d): %s", status, msg)
	}
	if resp.StopReason == "refusal" {
		return "", userErrorf("the model declined this request")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String(), nil
}

// hostitBackend asks hostit's container API. The app is identified by its uid on hostit's side,
// so there are no credentials. Without a configured model, hostit picks its default and falls
// back to the next model when one is rate limited.
type hostitBackend struct {
	url   string
	model string
	http  *http.Client
}

func (b *hostitBackend) name() string {
	return "hostit"
}

func (b *hostitBackend) complete(ctx context.Context, system, prompt string) (string, error) {
	body := map[string]any{"system": system, "prompt": prompt, "max_tokens": hostitTokens}
	if b.model != "" {
		body["model"] = b.model
	}
	var resp struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	status, err := postJSON(ctx, b.http, b.url+"/api/container/assistant", nil, body, &resp)
	if err != nil {
		return "", userErrorf("AI request failed: %v", err)
	}
	if status != http.StatusOK {
		msg := resp.Error
		if msg == "" {
			msg = http.StatusText(status)
		}
		return "", userErrorf("AI request failed (HTTP %d): %s", status, msg)
	}
	return resp.Text, nil
}

// hostitAvailable reports whether hostit's container API answers and has an assistant backend
// configured (it returns 501 otherwise).
func hostitAvailable(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/container/assistant/models", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// postJSON posts body as JSON and decodes the response into out, also for error statuses, so
// callers can read the error message. A response that isn't JSON is an error.
func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("HTTP %d: unexpected response %.200q", resp.StatusCode, raw)
	}
	return resp.StatusCode, nil
}

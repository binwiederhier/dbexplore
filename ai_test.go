package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHostit mimics hostit's container API. The handler gets the decoded ask request.
func fakeHostit(t *testing.T, modelsStatus int, ask func(w http.ResponseWriter, req map[string]any)) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/container/assistant/models":
			w.WriteHeader(modelsStatus)
			_, _ = io.WriteString(w, `{"models":[{"id":"claude-fable-5","label":"Fable 5","backend":"claude"}],"default":"claude-fable-5"}`)
		case r.Method == "POST" && r.URL.Path == "/api/container/assistant":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("invalid ask request: %v", err)
			}
			ask(w, req)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAssistantAnthropic(t *testing.T) {
	var got map[string]any
	var header http.Header
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","stop_reason":"end_turn",
			"content":[{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"`+"```sql\\nselect * from shop.customer\\n```"+`"}],
			"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer api.Close()
	a := newAssistantWith(&aiConfig{APIKey: "k", Model: "m", BaseURL: api.URL, Context: "an online shop"}, "http://127.0.0.1:1")
	if a == nil {
		t.Fatal("an API key must enable the assistant")
	}
	sql, err := a.writeSQL(context.Background(), "shop.customer(id bigint)\n", assistRequest{Prompt: "all customers", Table: "shop.customer"})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select * from shop.customer" {
		t.Errorf("sql = %q", sql)
	}
	if header.Get("x-api-key") != "k" || header.Get("anthropic-version") == "" {
		t.Errorf("missing auth or version header: %v", header)
	}
	if got["model"] != "m" {
		t.Errorf("model = %v", got["model"])
	}
	sys, _ := json.Marshal(got["system"])
	if !strings.Contains(string(sys), "an online shop") || !strings.Contains(string(sys), "shop.customer(id bigint)") || !strings.Contains(string(sys), "cache_control") {
		t.Errorf("system prompt misses context, schema or cache control: %s", sys)
	}
	if a := newAssistantWith(nil, "http://127.0.0.1:1"); a != nil {
		t.Error("no API key and no hostit means no assistant")
	}
}

func TestAssistantAnthropicDefaultModel(t *testing.T) {
	var got map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"select 1"}],"stop_reason":"end_turn"}`)
	}))
	defer api.Close()
	a := newAssistantWith(&aiConfig{APIKey: "k", BaseURL: api.URL}, "http://127.0.0.1:1")
	if _, err := a.writeSQL(context.Background(), "", assistRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if got["model"] != defaultModel {
		t.Errorf("model = %v, want %s", got["model"], defaultModel)
	}
}

func TestAssistantAnthropicError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer api.Close()
	a := newAssistantWith(&aiConfig{APIKey: "bad", BaseURL: api.URL}, "http://127.0.0.1:1")
	_, err := a.writeSQL(context.Background(), "", assistRequest{Prompt: "x"})
	var ue *userError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "invalid x-api-key") || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v", err)
	}
}

func TestAssistantHostit(t *testing.T) {
	var got map[string]any
	url := fakeHostit(t, http.StatusOK, func(w http.ResponseWriter, req map[string]any) {
		got = req
		_, _ = io.WriteString(w, `{"text":"`+"```sql\\nselect count(*) from shop.\\\"order\\\"\\n```"+`","model":"claude-fable-5"}`)
	})
	a := newAssistantWith(&aiConfig{Context: "an online shop"}, url)
	if a == nil {
		t.Fatal("hostit's container API must enable the assistant without a key")
	}
	sql, err := a.writeSQL(context.Background(), "shop.order(id bigint)\n", assistRequest{Prompt: "how many orders", CurrentSQL: "select 1"})
	if err != nil {
		t.Fatal(err)
	}
	if sql != `select count(*) from shop."order"` {
		t.Errorf("sql = %q", sql)
	}
	if sys, _ := got["system"].(string); !strings.Contains(sys, "an online shop") || !strings.Contains(sys, "shop.order(id bigint)") {
		t.Errorf("system = %q", sys)
	}
	if p, _ := got["prompt"].(string); !strings.Contains(p, "how many orders") || !strings.Contains(p, "select 1") {
		t.Errorf("prompt = %q", p)
	}
	if _, ok := got["model"]; ok {
		t.Errorf("no model configured, so hostit must pick it: %v", got["model"])
	}
	if a := newAssistantWith(nil, url); a == nil {
		t.Error("hostit must enable the assistant even without an ai section")
	}
}

func TestAssistantHostitNotConfigured(t *testing.T) {
	url := fakeHostit(t, http.StatusNotImplemented, nil)
	if a := newAssistantWith(nil, url); a != nil {
		t.Error("hostit without an assistant backend (501) must not enable Ask AI")
	}
}

func TestAssistantHostitError(t *testing.T) {
	url := fakeHostit(t, http.StatusOK, func(w http.ResponseWriter, req map[string]any) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"assistant rate limit reached, try again later"}`)
	})
	_, err := newAssistantWith(nil, url).writeSQL(context.Background(), "", assistRequest{Prompt: "x"})
	var ue *userError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("err = %v", err)
	}
}

func TestAssistantAPIKeyBeatsHostit(t *testing.T) {
	url := fakeHostit(t, http.StatusOK, func(w http.ResponseWriter, req map[string]any) {
		t.Error("hostit must not be called when an API key is configured")
	})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"select 2"}],"stop_reason":"end_turn"}`)
	}))
	defer api.Close()
	sql, err := newAssistantWith(&aiConfig{APIKey: "k", BaseURL: api.URL}, url).writeSQL(context.Background(), "", assistRequest{Prompt: "x"})
	if err != nil || sql != "select 2" {
		t.Errorf("sql = %q, err = %v", sql, err)
	}
}

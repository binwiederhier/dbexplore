package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextValue(t *testing.T) {
	cases := []struct {
		oid  uint32
		in   string
		want string
	}{
		{oidBool, "t", "true"},
		{oidInt4, "42", "42"},
		{oidInt8, "9007199254740993", `"9007199254740993"`},
		{oidInt8, "-12", "-12"},
		{oidNumeric, "894.00", "894.00"},
		{oidNumeric, "12345678901234567890", `"12345678901234567890"`},
		{oidFloat8, "NaN", `"NaN"`},
		{oidFloat8, "Infinity", `"Infinity"`},
		{oidJSONB, `{"a": 1}`, `{"a":1}`},
		{25, "hello", `"hello"`},
	}
	for _, c := range cases {
		out, _ := json.Marshal(textValue(c.oid, []byte(c.in)))
		if string(out) != c.want {
			t.Errorf("textValue(%d, %q) = %s, want %s", c.oid, c.in, out, c.want)
		}
	}
	if textValue(oidInt4, nil) != nil {
		t.Error("NULL must stay nil")
	}
}

func TestStatementFilters(t *testing.T) {
	for _, sql := range []string{"select 1", " WITH x as (select 1) select * from x", "values (1)", "table foo"} {
		if !pageableSQL.MatchString(sql) {
			t.Errorf("%q should page", sql)
		}
	}
	for _, sql := range []string{"explain select 1", "SHOW work_mem"} {
		if pageableSQL.MatchString(sql) || !readSQL.MatchString(sql) {
			t.Errorf("%q should run as-is", sql)
		}
	}
	for _, sql := range []string{"delete from t", "commit", "set x = 1", "selectx", "copy t to stdout"} {
		if readSQL.MatchString(sql) {
			t.Errorf("%q must be refused", sql)
		}
	}
	if got := stripSQL("  select 1 ; ;\n"); got != "select 1" {
		t.Errorf("stripSQL = %q", got)
	}
}

func TestPageArgs(t *testing.T) {
	if s, p := pageArgs(0, -3, 500); s != 100 || p != 0 {
		t.Errorf("defaults: %d %d", s, p)
	}
	if s, _ := pageArgs(5000, 0, 500); s != 500 {
		t.Errorf("clamp: %d", s)
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Password == "" || c.Secret == "" || c.Listen != defaultListen || c.MaxRows != defaultMaxRows {
		t.Fatalf("defaults not applied: %+v", c)
	}
	c2, err := loadConfig(path)
	if err != nil || c2.Password != c.Password {
		t.Fatalf("generated password must persist: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"password":"x","secret":"y","sources":[{"id":"a b","dsn":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("invalid source id must be rejected")
	}
}

func TestAssistant(t *testing.T) {
	var got map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"m","stop_reason":"end_turn",
			"content":[{"type":"text","text":"`+"```sql\\nselect * from shop.customer\\n```"+`"}],
			"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer api.Close()
	a := newAssistant(&aiConfig{APIKey: "k", Model: "m", BaseURL: api.URL, Context: "an online shop"})
	sql, err := a.writeSQL(context.Background(), "shop.customer(id bigint)\n", assistRequest{Prompt: "all customers", Table: "shop.customer"})
	if err != nil {
		t.Fatal(err)
	}
	if sql != "select * from shop.customer" {
		t.Errorf("sql = %q", sql)
	}
	sys, _ := json.Marshal(got["system"])
	if !strings.Contains(string(sys), "an online shop") || !strings.Contains(string(sys), "shop.customer(id bigint)") {
		t.Errorf("system prompt misses context or schema: %s", sys)
	}
	if newAssistant(nil) != nil {
		t.Error("no ai config means no assistant")
	}
}

// TestPostgres runs against a real database when DBEXPLORE_TEST_DSN is set, e.g.
// DBEXPLORE_TEST_DSN=postgres://user:pass@localhost:5432/postgres?sslmode=disable go test ./...
func TestPostgres(t *testing.T) {
	dsn := os.Getenv("DBEXPLORE_TEST_DSN")
	if dsn == "" {
		t.Skip("DBEXPLORE_TEST_DSN not set")
	}
	cfg := &config{Sources: []*sourceConfig{{ID: "t", DSN: dsn}}, MaxRows: 10, timeout: defaultTimeoutDuration()}
	d := newDB(cfg)
	defer d.close()
	ctx := context.Background()
	res, err := d.query(ctx, "t", "select g, g % 2 = 0, 'x' || g, null::int, '{\"k\":1}'::jsonb from generate_series(1, 20) g", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 10 || !res.HasMore {
		t.Fatalf("max_rows not applied: %d rows, has_more=%v", len(res.Rows), res.HasMore)
	}
	out, _ := json.Marshal(res.Rows[1])
	if string(out) != `[2,true,"x2",null,{"k":1}]` {
		t.Errorf("row = %s", out)
	}
	for _, sql := range []string{"create temp table x(i int)", "select 1; select 2", "select nextval('nope')"} {
		if _, err := d.query(ctx, "t", sql, 0); err == nil {
			t.Errorf("%q should fail", sql)
		}
	}
	if _, err := d.query(ctx, "t", "select set_config('transaction_read_only', 'off', true)", 0); err == nil {
		t.Error("leaving read-only mode must fail")
	}
}

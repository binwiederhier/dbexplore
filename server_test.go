package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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

func TestStatementFiltersSkipLeadingComments(t *testing.T) {
	for _, sql := range []string{"-- top customers\nselect 1", "/* a */ select 1", "/* a /* nested */ b */\n-- c\n  select 1", "( select 1 ) union (select 2)"} {
		if !pageableSQL.MatchString(stripLeading(sql)) {
			t.Errorf("%q should page", sql)
		}
	}
	if !readSQL.MatchString(stripLeading("-- plan\nexplain select 1")) {
		t.Error("explain after a comment should run")
	}
	for _, sql := range []string{"-- select\ndelete from t", "/* select */ delete from t", "/* select", "-- select"} {
		if readSQL.MatchString(stripLeading(sql)) {
			t.Errorf("%q must be refused", sql)
		}
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

func TestWriteJSONEncodeError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]any{"v": json.RawMessage(`{broken`)})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "error") {
		t.Errorf("an unencodable response must be a 500 with an error, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestLoginCookieSecure(t *testing.T) {
	s := &server{cfg: &config{Password: "pw", Secret: "s"}}
	for _, proto := range []string{"", "https"} {
		r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"password": {"pw"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		rec := httptest.NewRecorder()
		s.handleLogin(rec, r)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("proto %q: %d cookies", proto, len(cookies))
		}
		if cookies[0].Secure != (proto == "https") {
			t.Errorf("proto %q: Secure = %v", proto, cookies[0].Secure)
		}
	}
}

// demoServer returns a server with one source "demo" at $DBEXPLORE_TEST_DSN, which must have
// testdata/demo.sql loaded.
func demoServer(t *testing.T) (*server, http.Handler) {
	cfg := &config{Sources: []*sourceConfig{{ID: "demo", DSN: testDSN(t)}}, MaxRows: 10, timeout: defaultTimeoutDuration()}
	s := &server{cfg: cfg, db: newDB(cfg), cache: map[string]cacheEntry{}}
	t.Cleanup(s.db.close)
	res, err := s.db.query(context.Background(), "demo", "select to_regclass('shop.customer') is not null", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != true {
		t.Skip("testdata/demo.sql is not loaded")
	}
	mux := http.NewServeMux()
	s.routes(mux, http.NotFoundHandler())
	return s, mux
}

func call(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: invalid JSON response %q: %v", method, path, rec.Body.String(), err)
	}
	return rec.Code, out
}

func TestQueryExplainJSON(t *testing.T) {
	_, h := demoServer(t)
	code, out := call(t, h, "POST", "/api/query", map[string]any{"src": "demo", "sql": "explain (format json) select * from shop.customer"})
	if code != http.StatusOK || len(out["rows"].([]any)) != 1 {
		t.Fatalf("got %d %v", code, out)
	}
}

func TestQueryUnpagedIsTruncatedNotPaged(t *testing.T) {
	_, h := demoServer(t)
	code, out := call(t, h, "POST", "/api/query", map[string]any{"src": "demo", "sql": "show all"})
	if code != http.StatusOK {
		t.Fatalf("got %d %v", code, out)
	}
	if out["has_more"] != false || out["truncated"] != true || len(out["rows"].([]any)) != 10 {
		t.Errorf("SHOW/EXPLAIN cannot page, more rows than max_rows must be truncated: has_more=%v truncated=%v rows=%d", out["has_more"], out["truncated"], len(out["rows"].([]any)))
	}
}

func TestQueryLeadingComment(t *testing.T) {
	_, h := demoServer(t)
	code, out := call(t, h, "POST", "/api/query", map[string]any{"src": "demo", "sql": "-- all customers\nselect id from shop.customer order by id"})
	if code != http.StatusOK || out["has_more"] != true {
		t.Fatalf("got %d %v", code, out)
	}
}

func TestDDL(t *testing.T) {
	_, h := demoServer(t)
	ddl := func(schema, table string) string {
		code, out := call(t, h, "GET", "/api/ddl?src=demo&schema="+url.QueryEscape(schema)+"&table="+url.QueryEscape(table), nil)
		if code != http.StatusOK {
			t.Fatalf("ddl %s.%s: %d %v", schema, table, code, out)
		}
		return out["ddl"].(string)
	}
	order := ddl("shop", "order")
	for _, want := range []string{
		`CREATE TABLE "shop"."order" (`,
		`"id" bigint DEFAULT nextval(`,
		`"total" numeric(12,2) NOT NULL`,
		`CONSTRAINT "order_pkey" PRIMARY KEY (id)`,
		`CONSTRAINT "order_customer_id_fkey" FOREIGN KEY (customer_id) REFERENCES shop.customer(id)`,
		`CONSTRAINT "order_status_check" CHECK`,
		`CREATE INDEX order_customer_idx ON shop."order"`,
	} {
		if !strings.Contains(order, want) {
			t.Errorf("shop.order DDL misses %q:\n%s", want, order)
		}
	}
	if strings.Contains(order, "order_pkey ON") {
		t.Errorf("the primary key index must not be repeated:\n%s", order)
	}
	if v := ddl("shop", "customer_summary"); !strings.HasPrefix(v, `CREATE VIEW "shop"."customer_summary" AS`) || !strings.Contains(v, "shop.customer c") {
		t.Errorf("view DDL:\n%s", v)
	}
	if v := ddl("analytics", "daily_revenue"); !strings.HasPrefix(v, `CREATE MATERIALIZED VIEW "analytics"."daily_revenue" AS`) {
		t.Errorf("materialized view DDL:\n%s", v)
	}
	if v := ddl("analytics", "event"); !strings.Contains(v, ") PARTITION BY RANGE (at);") {
		t.Errorf("partitioned table DDL:\n%s", v)
	}
}

func TestAuthAllowsIcons(t *testing.T) {
	s := &server{cfg: &config{Password: "pw", Secret: "s"}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for _, path := range []string{"/favicon.svg", "/favicon.ico", "/apple-touch-icon.png", "/login.html"} {
		rec := httptest.NewRecorder()
		s.auth(ok).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s needs no login, got %d", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.auth(ok).ServeHTTP(rec, httptest.NewRequest("GET", "/app.js", nil))
	if rec.Code != http.StatusFound {
		t.Errorf("/app.js needs a login, got %d", rec.Code)
	}
}

func TestDecodeRequiresJSON(t *testing.T) {
	// A cross-site form can post text/plain without a preflight; it must not reach a handler
	for ct, ok := range map[string]bool{"application/json": true, "application/json; charset=utf-8": true, "text/plain": false, "": false} {
		r := httptest.NewRequest("POST", "/api/query", strings.NewReader(`{"src":"x"}`))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		var q queryRequest
		if err := decode(r, &q); (err == nil) != ok {
			t.Errorf("content type %q: err = %v", ct, err)
		}
	}
}

func callList(t *testing.T, s *server) (int, []map[string]any) {
	rec := httptest.NewRecorder()
	s.handleSources(rec, httptest.NewRequest("GET", "/api/sources", nil))
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return rec.Code, out
}

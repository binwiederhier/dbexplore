package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

func TestTextValueCopiesJSON(t *testing.T) {
	// pgx reuses the buffer behind RawValues for the next row, so a JSON value must not alias it
	buf := []byte(`{"a": 1}`)
	v := textValue(oidJSONB, buf)
	copy(buf, `{"b": 2}`)
	if out, _ := json.Marshal(v); string(out) != `{"a":1}` {
		t.Errorf("json value changed with the buffer: %s", out)
	}
}

// testDSN returns $DBEXPLORE_TEST_DSN or skips the test. Tests only read, some expect
// testdata/demo.sql to be loaded (see demoServer).
func testDSN(t *testing.T) string {
	dsn := os.Getenv("DBEXPLORE_TEST_DSN")
	if dsn == "" {
		t.Skip("DBEXPLORE_TEST_DSN not set")
	}
	return dsn
}

// TestPostgres runs against a real database when DBEXPLORE_TEST_DSN is set, e.g.
// DBEXPLORE_TEST_DSN=postgres://user:pass@localhost:5432/postgres?sslmode=disable go test ./...
func TestPostgres(t *testing.T) {
	dsn := testDSN(t)
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

func TestPostgresJSONRows(t *testing.T) {
	dsn := testDSN(t)
	cfg := &config{Sources: []*sourceConfig{{ID: "t", DSN: dsn}}, MaxRows: 1000, timeout: defaultTimeoutDuration()}
	d := newDB(cfg)
	defer d.close()
	res, err := d.query(context.Background(), "t", "select jsonb_build_object('i', g, 'pad', repeat('x', g * 50)) from generate_series(1, 300) g", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range res.Rows {
		var v struct{ I int }
		if err := json.Unmarshal(row[0].(json.RawMessage), &v); err != nil || v.I != i+1 {
			t.Fatalf("row %d: got i=%d, err=%v", i, v.I, err)
		}
	}
}

func TestPoolConnectTimeout(t *testing.T) {
	cfg := &config{Sources: []*sourceConfig{
		{ID: "default", DSN: "postgres://x@127.0.0.1:1/x"},
		{ID: "custom", DSN: "postgres://x@127.0.0.1:1/x?connect_timeout=3"},
	}, timeout: defaultTimeoutDuration()}
	d := newDB(cfg)
	defer d.close()
	for src, want := range map[string]time.Duration{"default": connectTimeout, "custom": 3 * time.Second} {
		p, err := d.pool(context.Background(), src)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Config().ConnConfig.ConnectTimeout; got != want {
			t.Errorf("%s: connect timeout %v, want %v", src, got, want)
		}
	}
}

// fakeContainerAPI mimics hostit's container API for one connection.
func fakeContainerAPI(t *testing.T, slug, meta, token string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/container/connections":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"slug": "other", "meta": "x"}, {"slug": slug, "meta": meta}})
		case "/api/container/connections/" + slug + "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestHostitConnectionDSN(t *testing.T) {
	api := fakeContainerAPI(t, "ntfy-db", "note=postgresql://db.example.com:25060", "secret")
	h := &hostitClient{url: api, http: http.DefaultClient}
	for dsn, want := range map[string]string{
		"postgres://hostit@/ntfy?sslmode=require":           "postgres://hostit@db.example.com:25060/ntfy?sslmode=require",
		"postgres://hostit@other:5432/ntfy?sslmode=require": "postgres://hostit@other:5432/ntfy?sslmode=require",
	} {
		got, err := h.dsn(context.Background(), "ntfy-db", dsn)
		if err != nil || got != want {
			t.Errorf("dsn(%q) = %q, %v; want %q", dsn, got, err, want)
		}
	}
	if _, err := h.dsn(context.Background(), "missing", "postgres://hostit@/ntfy"); err == nil {
		t.Error("an unknown connection must fail")
	}
	if tok, err := h.token(context.Background(), "ntfy-db"); err != nil || tok != "secret" {
		t.Errorf("token = %q, %v", tok, err)
	}
}

func TestPostgresHostitConnection(t *testing.T) {
	testDSN(t)
	// The demo database's explorer role; host and password only come from the connection
	api := fakeContainerAPI(t, "demo-db", "note=postgresql://127.0.0.1:55432", "explorer")
	cfg := &config{Sources: []*sourceConfig{{ID: "t", DSN: "postgres://explorer@/postgres?sslmode=disable", HostitConnection: "demo-db"}}, MaxRows: 10, timeout: defaultTimeoutDuration()}
	d := newDB(cfg)
	d.hostit = &hostitClient{url: api, http: http.DefaultClient}
	defer d.close()
	res, err := d.query(context.Background(), "t", "select current_user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] != "explorer" {
		t.Errorf("connected as %v", res.Rows[0][0])
	}
}

// testWriteDSN returns $DBEXPLORE_TEST_WRITE_DSN or skips the test. It must be a role that may
// write to the demo data (make demo-db creates "editor"); tests clean up the rows they add.
func testWriteDSN(t *testing.T) string {
	dsn := os.Getenv("DBEXPLORE_TEST_WRITE_DSN")
	if dsn == "" {
		t.Skip("DBEXPLORE_TEST_WRITE_DSN not set")
	}
	return dsn
}

func TestPostgresWrite(t *testing.T) {
	dsn := testWriteDSN(t)
	cfg := &config{Sources: []*sourceConfig{{ID: "rw", DSN: dsn, ReadWrite: true}, {ID: "ro", DSN: dsn}}, MaxRows: 10, timeout: defaultTimeoutDuration()}
	d := newDB(cfg)
	ctx := context.Background()
	cleanup := func() {
		_, _ = d.write(ctx, "rw", "delete from shop.customer where email = 'write-test@example.com'", 0, nil)
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		d.close()
	})
	insert := "insert into shop.customer (email, name, country) values ('write-test@example.com', 'w', 'US') returning id, pg_backend_pid()"
	if _, err := d.write(ctx, "ro", insert, 0, nil); err == nil {
		t.Fatal("a source without read_write must refuse writes")
	}

	// Committed, with the RETURNING rows and the command tag
	res, err := d.write(ctx, "rw", insert, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Command != "INSERT 0 1" || len(res.Rows) != 1 {
		t.Fatalf("command %q, rows %v", res.Command, res.Rows)
	}
	id, pid := res.Rows[0][0], res.Rows[0][1]
	if got, _ := d.query(ctx, "rw", "select count(*) from shop.customer where id = $1", 0, string(id.(json.Number))); got.Rows[0][0] != json.Number("1") {
		t.Fatalf("insert not committed: %v", got.Rows)
	}

	// A write's connection is not reused, so session state it set cannot leak into later queries
	res, err = d.write(ctx, "rw", "update shop.customer set name = 'w2' where email = 'write-test@example.com' returning pg_backend_pid()", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0] == pid {
		t.Error("the connection of the previous write was reused")
	}

	// A failing check rolls the statement back
	_, err = d.write(ctx, "rw", "update shop.customer set name = 'w3' where email = 'write-test@example.com'", 0, func(tag pgconn.CommandTag) error {
		return userErrorf("no")
	})
	if err == nil {
		t.Fatal("check error must be returned")
	}
	if got, _ := d.query(ctx, "rw", "select name from shop.customer where email = 'write-test@example.com'", 0); got.Rows[0][0] != "w2" {
		t.Errorf("a failed check must roll back, name = %v", got.Rows[0][0])
	}

	// Reads stay read-only on a read-write source
	if _, err := d.query(ctx, "rw", "delete from shop.customer where email = 'write-test@example.com'", 0); err == nil {
		t.Error("query must stay read-only on a read-write source")
	}
}

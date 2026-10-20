package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const writeTestEmail = "rows-test@example.com"

// writeServer returns a server with sources "rw" (read_write) and "ro" (not), both at
// $DBEXPLORE_TEST_WRITE_DSN with the demo data loaded. Rows with writeTestEmail are removed after
// the test.
func writeServer(t *testing.T) (*server, http.Handler) {
	dsn := testWriteDSN(t)
	cfg := &config{Sources: []*sourceConfig{{ID: "rw", DSN: dsn, ReadWrite: true}, {ID: "ro", DSN: dsn}}, MaxRows: 10, timeout: defaultTimeoutDuration()}
	s := &server{cfg: cfg, db: newDB(cfg), cache: map[string]cacheEntry{}}
	cleanup := func() {
		_, _ = s.db.write(context.Background(), "rw", "delete from shop.customer where email like 'rows-test%'", 0, nil)
	}
	cleanup()
	t.Cleanup(func() {
		cleanup()
		s.db.close()
	})
	mux := http.NewServeMux()
	s.routes(mux, http.NotFoundHandler())
	return s, mux
}

func TestQueryWrite(t *testing.T) {
	_, h := writeServer(t)
	query := func(src, sql string) (int, map[string]any) {
		return call(t, h, "POST", "/api/query", map[string]any{"src": src, "sql": sql})
	}
	insert := "insert into shop.customer (email, name, country) values ('" + writeTestEmail + "', 'r', 'US') returning id, email"

	// A read-only source refuses all writes, also inside a WITH
	if code, out := query("ro", insert); code != http.StatusBadRequest {
		t.Errorf("ro insert: %d %v", code, out)
	}

	// Writes run unpaged and report the command tag
	code, out := query("rw", "-- add one\n"+insert)
	if code != http.StatusOK || out["command"] != "INSERT 0 1" || len(out["rows"].([]any)) != 1 {
		t.Fatalf("rw insert: %d %v", code, out)
	}
	if code, out := query("rw", "update shop.customer set name = 'r2' where email = '"+writeTestEmail+"'"); code != http.StatusOK || out["command"] != "UPDATE 1" || len(out["columns"].([]any)) != 0 {
		t.Errorf("rw update: %d %v", code, out)
	}

	// A data-modifying WITH starts like a read: it fails read-only and then runs as a write
	withDelete := "with d as (delete from shop.customer where email = '" + writeTestEmail + "' returning email) select * from d"
	if code, out := query("ro", withDelete); code != http.StatusBadRequest {
		t.Errorf("ro with delete: %d %v", code, out)
	}
	code, out = query("rw", withDelete)
	if code != http.StatusOK || len(out["rows"].([]any)) != 1 || out["command"] != "SELECT 1" {
		t.Fatalf("rw with delete: %d %v", code, out)
	}

	// Plain reads stay read-only and paged
	code, out = query("rw", "select current_setting('transaction_read_only')")
	if code != http.StatusOK || out["rows"].([]any)[0].([]any)[0] != "on" || out["command"] != nil {
		t.Errorf("rw read: %d %v", code, out)
	}

	// Transaction control and session settings would outlive the statement
	for _, sql := range []string{"begin", "commit", "rollback", "savepoint x", "set search_path = x", "reset all", "discard all", "  /* c */ SET LOCAL statement_timeout = 0"} {
		if code, out := query("rw", sql); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "not allowed") {
			t.Errorf("%q: %d %v", sql, code, out)
		}
	}
}

func TestRows(t *testing.T) {
	_, h := writeServer(t)
	rows := func(op string, body map[string]any) (int, map[string]any) {
		body["schema"], body["table"] = "shop", "customer"
		if body["src"] == nil {
			body["src"] = "rw"
		}
		return call(t, h, "POST", "/api/rows/"+op, body)
	}
	col := func(out map[string]any, name string) any {
		for i, c := range out["columns"].([]any) {
			if c == name {
				return out["rows"].([]any)[0].([]any)[i]
			}
		}
		t.Fatalf("no column %q in %v", name, out)
		return nil
	}

	// Insert: omitted columns get their default, the new row comes back
	code, out := rows("insert", map[string]any{"values": map[string]any{"email": writeTestEmail, "name": "Rows Test", "country": "DE", "tags": "{a,b}", "prefs": `{"x": 1}`}})
	if code != http.StatusOK {
		t.Fatalf("insert: %d %v", code, out)
	}
	id := col(out, "id")
	if col(out, "vip") != false || col(out, "country") != "DE" {
		t.Errorf("inserted row: %v", out)
	}
	if code, out := rows("insert", map[string]any{"src": "ro", "values": map[string]any{"email": "rows-test-2@example.com", "name": "x", "country": "DE"}}); code != http.StatusBadRequest {
		t.Errorf("ro insert: %d %v", code, out)
	}

	// Update by primary key, with type casts and NULL
	key := map[string]any{"id": id}
	code, out = rows("update", map[string]any{"key": key, "set": map[string]any{"vip": "true", "prefs": nil, "name": "O'Brien"}})
	if code != http.StatusOK || col(out, "vip") != true || col(out, "prefs") != nil || col(out, "name") != "O'Brien" {
		t.Fatalf("update: %d %v", code, out)
	}
	if code, out := rows("update", map[string]any{"key": map[string]any{"id": json.Number("999999999")}, "set": map[string]any{"name": "x"}}); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "0 rows") {
		t.Errorf("update of a missing row: %d %v", code, out)
	}
	if code, out := rows("update", map[string]any{"key": map[string]any{"email": writeTestEmail}, "set": map[string]any{"name": "x"}}); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "primary key") {
		t.Errorf("update by a non-key column: %d %v", code, out)
	}
	if code, out := rows("update", map[string]any{"key": key, "set": map[string]any{"nope": "x"}}); code != http.StatusBadRequest {
		t.Errorf("update of an unknown column: %d %v", code, out)
	}
	if code, out := rows("update", map[string]any{"key": key, "set": map[string]any{"country": "too long"}}); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "ERROR") {
		t.Errorf("a database error must come back: %d %v", code, out)
	}
	if code, out := rows("update", map[string]any{"src": "ro", "key": key, "set": map[string]any{"name": "x"}}); code != http.StatusBadRequest {
		t.Errorf("ro update: %d %v", code, out)
	}

	// Delete exactly the one row
	if code, out := rows("delete", map[string]any{"src": "ro", "key": key}); code != http.StatusBadRequest {
		t.Errorf("ro delete: %d %v", code, out)
	}
	if code, out := rows("delete", map[string]any{"key": key}); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, out)
	}
	if code, out := rows("delete", map[string]any{"key": key}); code != http.StatusBadRequest {
		t.Errorf("second delete: %d %v", code, out)
	}
}

func TestSourcesReportReadWrite(t *testing.T) {
	s := &server{cfg: &config{Sources: []*sourceConfig{{ID: "a", Name: "a", ReadWrite: true}, {ID: "b", Name: "b"}}}}
	_, out := callList(t, s)
	if out[0]["read_write"] != true || out[1]["read_write"] != false {
		t.Errorf("sources: %v", out)
	}
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs that mean a read failed only because the statement writes
const (
	sqlStateReadOnly     = "25006" // read_only_sql_transaction
	sqlStateNotSupported = "0A000" // feature_not_supported, e.g. a data-modifying WITH wrapped for paging
)

// sessionSQL matches statements that would outlive the one transaction a write runs in.
var sessionSQL = regexp.MustCompile(`(?is)^(begin|start|commit|end|rollback|abort|savepoint|release|prepare|set|reset|discard)\b`)

// needsWrite reports whether a read failed only because the statement writes: a data-modifying
// WITH or a function like nextval() in a read-only transaction, or such a WITH wrapped for paging.
func needsWrite(err error) bool {
	var ue *userError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.code == sqlStateReadOnly || ue.code == sqlStateNotSupported && strings.Contains(ue.msg, "data-modifying statement")
}

// writeQuery runs a console statement on a read_write source as a committed write. It cannot page,
// so more than max_rows returned rows are truncated.
func (s *server) writeQuery(w http.ResponseWriter, r *http.Request, src, sql string) {
	if sessionSQL.MatchString(stripLeading(sql)) {
		fail(w, userErrorf("transaction control and session settings (BEGIN, COMMIT, SET, ...) are not allowed: every statement runs and commits in its own transaction"))
		return
	}
	res, err := s.db.write(r.Context(), src, sql, 0, nil)
	if err != nil {
		fail(w, err)
		return
	}
	s.forget(src) // the statement may have changed tables or columns
	res.SQL = sql
	res.Truncated, res.HasMore = res.HasMore, false
	writeJSON(w, res)
}

// forget drops the cached metadata of a source.
func (s *server) forget(src string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.cache {
		if _, rest, _ := strings.Cut(k, ":"); rest == src || strings.HasPrefix(rest, src+":") {
			delete(s.cache, k)
		}
	}
}

type rowsRequest struct {
	Src    string         `json:"src"`
	Schema string         `json:"schema"`
	Table  string         `json:"table"`
	Key    map[string]any `json:"key"`    // update, delete: the primary key of the row
	Set    map[string]any `json:"set"`    // update: the changed columns
	Values map[string]any `json:"values"` // insert: the given columns, the rest get their default
}

// handleRows inserts, updates or deletes a single row of a read_write source. Values are text (or
// null) and parsed by the database as the column type, so they take the same input syntax as SQL
// literals. Updates and deletes address the row by its full primary key and roll back unless they
// match exactly one row.
func (s *server) handleRows(w http.ResponseWriter, r *http.Request) {
	var q rowsRequest
	if err := decode(r, &q); err != nil {
		fail(w, err)
		return
	}
	if src := s.cfg.source(q.Src); src == nil || !src.ReadWrite {
		fail(w, userErrorf("source %q is read-only", q.Src))
		return
	}
	rel := qrel(q.Schema, q.Table)
	cols, err := s.columns(r.Context(), q.Src, rel)
	if err != nil {
		fail(w, err)
		return
	}
	b := &rowSQL{cols: map[string]column{}}
	for _, c := range cols {
		b.cols[c.Name] = c
	}
	var sql string
	var check func(pgconn.CommandTag) error
	switch r.PathValue("op") {
	case "insert":
		sql, err = b.insert(rel, q.Values)
	case "update":
		sql, err = b.update(rel, cols, q.Key, q.Set)
		check = exactlyOne
	case "delete":
		sql, err = b.delete(rel, cols, q.Key)
		check = exactlyOne
	default:
		err = userErrorf("unknown operation %q", r.PathValue("op"))
	}
	if err != nil {
		fail(w, err)
		return
	}
	res, err := s.db.write(r.Context(), q.Src, sql, 1, check, b.args...)
	if err != nil {
		fail(w, err)
		return
	}
	res.SQL = sql
	writeJSON(w, res)
}

func exactlyOne(tag pgconn.CommandTag) error {
	if n := tag.RowsAffected(); n != 1 {
		return userErrorf("expected to change 1 row, but the statement matched %d rows; nothing was changed", n)
	}
	return nil
}

// rowSQL builds one statement for handleRows, collecting its arguments.
type rowSQL struct {
	cols map[string]column
	args []any
}

func (b *rowSQL) insert(rel string, values map[string]any) (string, error) {
	if len(values) == 0 {
		return "insert into " + rel + " default values returning *", nil
	}
	var names, vals []string
	for _, name := range slices.Sorted(maps.Keys(values)) {
		v, err := b.arg(name, values[name])
		if err != nil {
			return "", err
		}
		names, vals = append(names, qi(name)), append(vals, v)
	}
	return fmt.Sprintf("insert into %s (%s) values (%s) returning *", rel, strings.Join(names, ", "), strings.Join(vals, ", ")), nil
}

func (b *rowSQL) update(rel string, cols []column, key, set map[string]any) (string, error) {
	if len(set) == 0 {
		return "", userErrorf("nothing to update")
	}
	var sets []string
	for _, name := range slices.Sorted(maps.Keys(set)) {
		v, err := b.arg(name, set[name])
		if err != nil {
			return "", err
		}
		sets = append(sets, qi(name)+" = "+v)
	}
	where, err := b.where(rel, cols, key)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("update %s set %s where %s returning *", rel, strings.Join(sets, ", "), where), nil
}

func (b *rowSQL) delete(rel string, cols []column, key map[string]any) (string, error) {
	where, err := b.where(rel, cols, key)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("delete from %s where %s", rel, where), nil
}

// where matches the primary key. The key must name exactly the primary key columns, so a stale
// or partial key can never match more than the one row.
func (b *rowSQL) where(rel string, cols []column, key map[string]any) (string, error) {
	var pk []string
	for _, c := range cols {
		if c.PK {
			pk = append(pk, c.Name)
		}
	}
	if len(pk) == 0 {
		return "", userErrorf("%s has no primary key; change it with a statement in a console", rel)
	}
	match := len(key) == len(pk)
	for _, name := range pk {
		_, ok := key[name]
		match = match && ok
	}
	if !match {
		return "", userErrorf("the key must be the primary key of %s (%s)", rel, strings.Join(pk, ", "))
	}
	var conds []string
	for _, name := range pk {
		v, err := b.arg(name, key[name])
		if err != nil {
			return "", err
		}
		conds = append(conds, qi(name)+" = "+v)
	}
	return strings.Join(conds, " and "), nil
}

// arg adds a value for a column and returns its placeholder. The placeholder stays untyped, so
// Postgres infers the column's type and assigns the text like a literal: an explicit cast would
// silently truncate a too long value for char(n) and varchar(n).
func (b *rowSQL) arg(name string, v any) (string, error) {
	if _, ok := b.cols[name]; !ok {
		return "", userErrorf("unknown column %q", name)
	}
	b.args = append(b.args, textArg(v))
	return fmt.Sprintf("$%d", len(b.args)), nil
}

// textArg turns a JSON value into its Postgres text input: strings as they are, JSON objects and
// arrays (json and jsonb columns) serialized, null as NULL.
func textArg(v any) any {
	switch v := v.(type) {
	case nil:
		return nil
	case string:
		return v
	case json.Number:
		return v.String()
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

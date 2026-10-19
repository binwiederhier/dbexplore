package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// result is one query's output as the frontend expects it: column names and rows of JSON values.
type result struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	HasMore bool     `json:"has_more"`
	MS      int64    `json:"ms"`
	SQL     string   `json:"sql,omitempty"`
}

// userError is an error worth showing the user verbatim (bad SQL, unknown table, timeout).
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// db holds one lazily created connection pool per source.
type db struct {
	cfg   *config
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

func newDB(cfg *config) *db {
	return &db{cfg: cfg, pools: map[string]*pgxpool.Pool{}}
}

func (d *db) pool(ctx context.Context, src string) (*pgxpool.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.pools[src]; p != nil {
		return p, nil
	}
	s := d.cfg.source(src)
	if s == nil {
		return nil, userErrorf("unknown source %q", src)
	}
	pc, err := pgxpool.ParseConfig(s.DSN)
	if err != nil {
		return nil, userErrorf("source %q: invalid dsn: %v", src, err)
	}
	pc.MaxConns = 4
	pc.MaxConnIdleTime = 5 * time.Minute
	// Extended protocol, never the simple one: Postgres then refuses more than one statement per
	// query, so "select 1; delete from t" cannot sneak past the read-only transaction.
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec
	pc.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pc.ConnConfig.RuntimeParams["application_name"] = "dbexplore"
	p, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	d.pools[src] = p
	return p, nil
}

func (d *db) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.pools {
		p.Close()
	}
}

// query runs one statement in a read-only transaction with a statement timeout and returns at most
// limit rows (limit <= 0 means max_rows; callers clamp user-chosen limits). Results are requested in text format and converted to
// JSON values by column type, so every Postgres type (including extensions) renders.
func (d *db) query(ctx context.Context, src, sql string, limit int, args ...any) (*result, error) {
	if limit <= 0 {
		limit = d.cfg.MaxRows
	}
	p, err := d.pool(ctx, src)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.timeout+5*time.Second)
	defer cancel()
	t0 := time.Now()
	tx, err := p.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src, err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := tx.Exec(ctx, "select set_config('statement_timeout', $1, true)", strconv.FormatInt(d.cfg.timeout.Milliseconds(), 10)); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, append([]any{pgx.QueryResultFormatsByOID{}}, args...)...)
	if err != nil {
		return nil, pgError(err)
	}
	defer rows.Close()
	res := &result{Columns: []string{}, Rows: [][]any{}}
	fields := rows.FieldDescriptions()
	for _, f := range fields {
		res.Columns = append(res.Columns, f.Name)
	}
	for rows.Next() {
		if len(res.Rows) == limit {
			res.HasMore = true
			break
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		for i, v := range raw {
			row[i] = textValue(fields[i].DataTypeOID, v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, pgError(err)
	}
	res.MS = time.Since(t0).Milliseconds()
	return res, nil
}

func pgError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := pe.Message
		if pe.Detail != "" {
			msg += "\n" + pe.Detail
		}
		if pe.Hint != "" {
			msg += "\nHint: " + pe.Hint
		}
		if pe.Position > 0 {
			msg += fmt.Sprintf("\n(at character %d)", pe.Position)
		}
		return &userError{msg: "ERROR: " + msg}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return userErrorf("query timed out")
	}
	return err
}

// Type OIDs that map to JSON numbers, booleans and objects. Everything else stays a string.
const (
	oidBool    = 16
	oidInt8    = 20
	oidInt2    = 21
	oidInt4    = 23
	oidOID     = 26
	oidJSON    = 114
	oidFloat4  = 700
	oidFloat8  = 701
	oidNumeric = 1700
	oidJSONB   = 3802
)

var plainNumber = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)

func textValue(oid uint32, v []byte) any {
	if v == nil {
		return nil
	}
	s := string(v)
	switch oid {
	case oidBool:
		return s == "t"
	case oidInt2, oidInt4, oidOID:
		return json.Number(s)
	case oidInt8, oidNumeric, oidFloat4, oidFloat8:
		// JavaScript numbers are doubles: keep values that would lose precision (and NaN/Infinity) as strings.
		if !plainNumber.MatchString(s) || len(strings.TrimLeft(strings.Replace(s, ".", "", 1), "-0")) > 15 {
			return s
		}
		return json.Number(s)
	case oidJSON, oidJSONB:
		if json.Valid(v) {
			return json.RawMessage(v)
		}
	}
	return s
}

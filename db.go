package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// Truncated is set instead of HasMore for statements that cannot page (EXPLAIN, SHOW)
	Truncated bool   `json:"truncated,omitempty"`
	MS        int64  `json:"ms"`
	SQL       string `json:"sql,omitempty"`
	Command   string `json:"command,omitempty"`
}

// userError is an error worth showing the user verbatim (bad SQL, unknown table, timeout).
type userError struct {
	msg  string
	code string // SQLSTATE if the database raised it
}

func (e *userError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// connectTimeout applies when the DSN sets no connect_timeout, so an unreachable host fails fast
// instead of hanging until the request times out.
const connectTimeout = 10 * time.Second

// db holds one lazily created connection pool per source.
type db struct {
	cfg    *config
	hostit *hostitClient
	mu     sync.Mutex
	pools  map[string]*pgxpool.Pool
}

func newDB(cfg *config) *db {
	return &db{cfg: cfg, hostit: &hostitClient{url: hostitURL, http: &http.Client{Timeout: 10 * time.Second}}, pools: map[string]*pgxpool.Pool{}}
}

func (d *db) pool(ctx context.Context, src string) (*pgxpool.Pool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p := d.pools[src]; p != nil {
		return p, nil
	}
	var err error
	s := d.cfg.source(src)
	if s == nil {
		return nil, userErrorf("unknown source %q", src)
	}
	dsn := s.DSN
	if s.HostitConnection != "" {
		if dsn, err = d.hostit.dsn(ctx, s.HostitConnection, dsn); err != nil {
			return nil, fmt.Errorf("source %q: %w", src, err)
		}
	}
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, userErrorf("source %q: invalid dsn: %v", src, err)
	}
	if slug := s.HostitConnection; slug != "" {
		// Fetched for every new connection, so a password rotated in hostit is picked up
		pc.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
			password, err := d.hostit.token(ctx, slug)
			if err != nil {
				return err
			}
			cc.Password = password
			return nil
		}
	}
	pc.MaxConns = 4
	if pc.ConnConfig.ConnectTimeout == 0 {
		pc.ConnConfig.ConnectTimeout = connectTimeout
	}
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
// limit rows (limit <= 0 means max_rows; callers clamp user-chosen limits).
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
	res, err := collect(rows, limit)
	if err != nil {
		return nil, err
	}
	res.MS = time.Since(t0).Milliseconds()
	return res, nil
}

// write runs one statement in a read-write transaction and commits it, unless check (called with
// the command tag) returns an error. Only sources with read_write may write. The connection is
// closed afterwards instead of going back to the pool, so session state the statement changed
// (settings, temp tables, locks) cannot leak into later queries.
func (d *db) write(ctx context.Context, src, sql string, limit int, check func(pgconn.CommandTag) error, args ...any) (*result, error) {
	if s := d.cfg.source(src); s == nil || !s.ReadWrite {
		return nil, userErrorf("source %q is read-only", src)
	}
	if limit <= 0 {
		limit = d.cfg.MaxRows
	}
	p, err := d.pool(ctx, src)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.timeout+5*time.Second)
	defer cancel()
	c, err := p.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", src, err)
	}
	conn := c.Hijack()
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck
	t0 := time.Now()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
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
	res, err := collect(rows, limit)
	if err != nil {
		return nil, err
	}
	tag := rows.CommandTag()
	if check != nil {
		if err := check(tag); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, pgError(err)
	}
	res.Command = tag.String()
	res.MS = time.Since(t0).Milliseconds()
	return res, nil
}

// collect reads at most limit rows and closes rows. Results are requested in text format and
// converted to JSON values by column type, so every Postgres type (including extensions) renders.
func collect(rows pgx.Rows, limit int) (*result, error) {
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
	rows.Close() // reads the rest of the result, so the command tag is complete
	if err := rows.Err(); err != nil {
		return nil, pgError(err)
	}
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
		return &userError{msg: "ERROR: " + msg, code: pe.Code}
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
			return json.RawMessage(bytes.Clone(v)) // v is pgx's read buffer, reused for the next row
		}
	}
	return s
}

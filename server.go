package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cookieName = "dbexplore"
	metaTTL    = 10 * time.Minute
)

type server struct {
	cfg *config
	db  *db
	ai  *assistant // nil if no API key is configured

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	expires time.Time
	val     any
}

func (s *server) routes(mux *http.ServeMux, static http.Handler) {
	mux.Handle("GET /", static)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/columns", s.handleColumns)
	mux.HandleFunc("GET /api/fks", s.handleFKs)
	mux.HandleFunc("GET /api/ddl", s.handleDDL)
	mux.HandleFunc("POST /api/table", s.handleTable)
	mux.HandleFunc("POST /api/count", s.handleCount)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	mux.HandleFunc("POST /api/assist", s.handleAssist)
}

// ---- auth ----

func (s *server) sessionToken() string {
	m := hmac.New(sha256.New, []byte(s.cfg.Secret))
	m.Write([]byte("session:" + s.cfg.Password))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.NoAuth || r.URL.Path == "/login" || r.URL.Path == "/login.html" || r.URL.Path == "/favicon.svg" {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.sessionToken())) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
		}
		http.Redirect(w, r, "/login.html", http.StatusFound)
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(s.cfg.Password)) != 1 {
		time.Sleep(time.Second)
		http.Redirect(w, r, "/login.html?failed=1", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.sessionToken(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   90 * 24 * 3600,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// fail maps an error to a response: user errors (bad SQL, unknown source) are 400 and shown
// verbatim, anything else is a 502 since it is almost always the database being unreachable.
func fail(w http.ResponseWriter, err error) {
	var ue *userError
	if errors.As(err, &ue) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	slog.Warn("request failed", "error", err)
	writeErr(w, http.StatusBadGateway, err)
}

func (s *server) cached(key string, fresh bool, load func() (any, error)) (any, error) {
	s.mu.Lock()
	e, ok := s.cache[key]
	s.mu.Unlock()
	if ok && !fresh && time.Now().Before(e.expires) {
		return e.val, nil
	}
	v, err := load()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cacheEntry{expires: time.Now().Add(metaTTL), val: v}
	s.mu.Unlock()
	return v, nil
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return userErrorf("invalid request: %v", err)
	}
	return nil
}

func qi(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func pageArgs(size, page, maxRows int) (int, int) {
	if size <= 0 {
		size = 100
	}
	return min(size, maxRows), max(page, 0)
}

// ---- handlers ----

type sourceInfo struct {
	ID   string `json:"id"`
	Name string `json:"label"`
	AI   bool   `json:"ai"`
}

func (s *server) handleSources(w http.ResponseWriter, r *http.Request) {
	out := []sourceInfo{}
	for _, src := range s.cfg.Sources {
		out = append(out, sourceInfo{ID: src.ID, Name: src.Name, AI: s.ai != nil})
	}
	writeJSON(w, out)
}

type treeTable struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Est  any    `json:"est"`
}

type treeSchema struct {
	Name   string       `json:"name"`
	Tables []*treeTable `json:"tables"`
}

func (s *server) handleTree(w http.ResponseWriter, r *http.Request) {
	src := r.FormValue("src")
	v, err := s.cached("tree:"+src, r.FormValue("refresh") == "1", func() (any, error) {
		res, err := s.db.query(r.Context(), src, `
			select n.nspname, c.relname, c.relkind::text, greatest(c.reltuples, -1)::bigint
			from pg_class c join pg_namespace n on n.oid = c.relnamespace
			where c.relkind in ('r','v','m','p','f') and not c.relispartition
			order by n.nspname, c.relname`, 100000)
		if err != nil {
			return nil, err
		}
		bySchema := map[string]*treeSchema{}
		var schemas []*treeSchema
		for _, row := range res.Rows {
			nsp := row[0].(string)
			sc := bySchema[nsp]
			if sc == nil {
				sc = &treeSchema{Name: nsp, Tables: []*treeTable{}}
				bySchema[nsp] = sc
				schemas = append(schemas, sc)
			}
			sc.Tables = append(sc.Tables, &treeTable{Name: row[1].(string), Kind: row[2].(string), Est: row[3]})
		}
		system := func(n string) bool {
			return n == "pg_catalog" || n == "information_schema" || strings.HasPrefix(n, "pg_toast")
		}
		sort.SliceStable(schemas, func(i, j int) bool {
			if system(schemas[i].Name) != system(schemas[j].Name) {
				return !system(schemas[i].Name)
			}
			return schemas[i].Name < schemas[j].Name
		})
		return map[string]any{"schemas": schemas, "truncated": res.HasMore}, nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, v)
}

type column struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"notnull"`
	PK      bool   `json:"pk"`
}

func relName(r *http.Request) string {
	return qi(r.FormValue("schema")) + "." + qi(r.FormValue("table"))
}

func (s *server) columns(r *http.Request) ([]column, error) {
	src, rel := r.FormValue("src"), relName(r)
	v, err := s.cached("cols:"+src+":"+rel, false, func() (any, error) {
		res, err := s.db.query(r.Context(), src, `
			select a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull,
			       coalesce((select true from pg_index i where i.indrelid = a.attrelid and i.indisprimary and a.attnum = any(i.indkey)), false)
			from pg_attribute a
			where a.attrelid = $1::regclass and a.attnum > 0 and not a.attisdropped
			order by a.attnum`, 0, rel)
		if err != nil {
			return nil, err
		}
		cols := []column{}
		for _, row := range res.Rows {
			cols = append(cols, column{Name: row[0].(string), Type: row[1].(string), NotNull: row[2].(bool), PK: row[3].(bool)})
		}
		return cols, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]column), nil
}

func (s *server) handleColumns(w http.ResponseWriter, r *http.Request) {
	cols, err := s.columns(r)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, cols)
}

type foreignKey struct {
	Name      string   `json:"name"`
	Cols      []string `json:"cols"`
	RefSchema string   `json:"ref_schema"`
	RefTable  string   `json:"ref_table"`
	RefCols   []string `json:"ref_cols"`
}

func (s *server) handleFKs(w http.ResponseWriter, r *http.Request) {
	src, rel := r.FormValue("src"), relName(r)
	v, err := s.cached("fks:"+src+":"+rel, false, func() (any, error) {
		names := func(relCol, keyCol string) string {
			return fmt.Sprintf("(select array_agg(a.attname::text order by k.i) from unnest(con.%s) with ordinality k(n, i) join pg_attribute a on a.attrelid = con.%s and a.attnum = k.n)", keyCol, relCol)
		}
		res, err := s.db.query(r.Context(), src, `
			select con.conname, array_to_json(`+names("conrelid", "conkey")+`), fn.nspname, fc.relname, array_to_json(`+names("confrelid", "confkey")+`)
			from pg_constraint con
			join pg_class fc on fc.oid = con.confrelid
			join pg_namespace fn on fn.oid = fc.relnamespace
			where con.contype = 'f' and con.conrelid = $1::regclass
			order by con.conname`, 0, rel)
		if err != nil {
			return nil, err
		}
		fks := []foreignKey{}
		for _, row := range res.Rows {
			fk := foreignKey{Name: row[0].(string), RefSchema: row[2].(string), RefTable: row[3].(string)}
			_ = json.Unmarshal(row[1].(json.RawMessage), &fk.Cols)
			_ = json.Unmarshal(row[4].(json.RawMessage), &fk.RefCols)
			fks = append(fks, fk)
		}
		return fks, nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, v)
}

func (s *server) handleDDL(w http.ResponseWriter, r *http.Request) {
	cols, err := s.columns(r)
	if err != nil {
		fail(w, err)
		return
	}
	idx, err := s.db.query(r.Context(), r.FormValue("src"), "select indexdef from pg_indexes where schemaname = $1 and tablename = $2 order by indexname", 0, r.FormValue("schema"), r.FormValue("table"))
	if err != nil {
		fail(w, err)
		return
	}
	var lines, pk []string
	for _, c := range cols {
		l := "  " + qi(c.Name) + " " + c.Type
		if c.NotNull {
			l += " NOT NULL"
		}
		lines = append(lines, l)
		if c.PK {
			pk = append(pk, qi(c.Name))
		}
	}
	if len(pk) > 0 {
		lines = append(lines, "  PRIMARY KEY ("+strings.Join(pk, ", ")+")")
	}
	out := "CREATE TABLE " + relName(r) + " (\n" + strings.Join(lines, ",\n") + "\n);\n"
	for _, row := range idx.Rows {
		out += row[0].(string) + ";\n"
	}
	writeJSON(w, map[string]string{"ddl": out})
}

type tableRequest struct {
	Src      string `json:"src"`
	Schema   string `json:"schema"`
	Table    string `json:"table"`
	Where    string `json:"where"`
	Order    string `json:"order"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

func (t *tableRequest) from() string {
	sql := "select * from " + qi(t.Schema) + "." + qi(t.Table)
	if w := strings.TrimSpace(t.Where); w != "" {
		sql += " where (" + w + ")"
	}
	return sql
}

// paged runs base with limit/offset. It asks for one row more than the page so the result knows
// whether there is a next page.
func (s *server) paged(r *http.Request, src, base string, size, page int) (*result, error) {
	sql := fmt.Sprintf("%s limit %d offset %d", base, size+1, size*page)
	res, err := s.db.query(r.Context(), src, sql, size)
	if err != nil {
		return nil, err
	}
	res.SQL = sql
	return res, nil
}

func (s *server) handleTable(w http.ResponseWriter, r *http.Request) {
	var t tableRequest
	if err := decode(r, &t); err != nil {
		fail(w, err)
		return
	}
	size, page := pageArgs(t.PageSize, t.Page, s.cfg.MaxRows)
	base := t.from()
	if o := strings.TrimSpace(t.Order); o != "" {
		base += " order by " + o
	}
	res, err := s.paged(r, t.Src, base, size, page)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}

func (s *server) handleCount(w http.ResponseWriter, r *http.Request) {
	var t tableRequest
	if err := decode(r, &t); err != nil {
		fail(w, err)
		return
	}
	res, err := s.db.query(r.Context(), t.Src, "select count(*) from ("+t.from()+") q", 1)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]any{"count": res.Rows[0][0]})
}

var (
	pageableSQL = regexp.MustCompile(`(?is)^\s*(select|with|values|table)\b`)
	readSQL     = regexp.MustCompile(`(?is)^\s*(select|with|values|table|explain|show)\b`)
)

func stripSQL(sql string) string {
	sql = strings.TrimSpace(sql)
	for strings.HasSuffix(sql, ";") {
		sql = strings.TrimSpace(strings.TrimSuffix(sql, ";"))
	}
	return sql
}

type queryRequest struct {
	Src      string `json:"src"`
	SQL      string `json:"sql"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var q queryRequest
	if err := decode(r, &q); err != nil {
		fail(w, err)
		return
	}
	sql := stripSQL(q.SQL)
	switch {
	case sql == "":
		fail(w, userErrorf("empty query"))
	case pageableSQL.MatchString(sql):
		// Wrapped so it pages; the newline ends a trailing -- comment in the user's SQL.
		size, page := pageArgs(q.PageSize, q.Page, s.cfg.MaxRows)
		res, err := s.paged(r, q.Src, "select * from ("+sql+"\n) q", size, page)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, res)
	case readSQL.MatchString(sql):
		res, err := s.db.query(r.Context(), q.Src, sql, 0)
		if err != nil {
			fail(w, err)
			return
		}
		res.SQL = sql
		writeJSON(w, res)
	default:
		fail(w, userErrorf("only SELECT, WITH, VALUES, TABLE, EXPLAIN and SHOW statements are allowed"))
	}
}

type assistRequest struct {
	Src        string `json:"src"`
	Prompt     string `json:"prompt"`
	CurrentSQL string `json:"current_sql"`
	Table      string `json:"table"`
}

func (s *server) handleAssist(w http.ResponseWriter, r *http.Request) {
	if s.ai == nil {
		fail(w, userErrorf("AI query generation is not configured: set ai.api_key in the config"))
		return
	}
	var a assistRequest
	if err := decode(r, &a); err != nil {
		fail(w, err)
		return
	}
	if strings.TrimSpace(a.Prompt) == "" {
		fail(w, userErrorf("empty prompt"))
		return
	}
	schema, err := s.schemaSummary(r, a.Src)
	if err != nil {
		fail(w, err)
		return
	}
	sql, err := s.ai.writeSQL(r.Context(), schema, a)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]string{"sql": sql})
}

// schemaSummary lists every non-system table as "schema.table(column type, ...)", one per line,
// for the AI prompt.
func (s *server) schemaSummary(r *http.Request, src string) (string, error) {
	v, err := s.cached("summary:"+src, false, func() (any, error) {
		res, err := s.db.query(r.Context(), src, `
			select table_schema || '.' || table_name, string_agg(column_name || ' ' || data_type, ', ' order by ordinal_position)
			from information_schema.columns
			where table_schema not in ('pg_catalog', 'information_schema')
			group by 1 order by 1`, 100000)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, row := range res.Rows {
			fmt.Fprintf(&b, "%s(%s)\n", row[0], row[1])
		}
		return b.String(), nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

# Architecture

dbexplore is one Go binary that serves an embedded single-page UI and a small JSON API. It talks to PostgreSQL directly with [pgx](https://github.com/jackc/pgx), and optionally to Claude for Ask AI, either through the Anthropic API or through hostit's container API.

```mermaid
flowchart LR
  B["Browser<br/>web/app.js"] -- "HTTP + session cookie" --> A
  subgraph S [dbexplore]
    A["auth middleware<br/>server.go"] --> H["API handlers<br/>server.go"]
    H --> C[("metadata cache")]
    H --> D["db.go<br/>one pgx pool per source"]
    H --> AI["ai.go"]
  end
  D -- "read-only tx" --> P1[("Postgres A")]
  D -- "read-only tx" --> P2[("Postgres B")]
  AI -- "Messages API" --> CL["Claude"]
```

## Source files

| File | Responsibility |
| --- | --- |
| `main.go` | Flags (`-config`, `-print-password`, `-version`), starts the HTTP server, graceful shutdown. |
| `config.go` | Load the config, apply defaults, generate the password/secret on first start, validate sources. |
| `db.go` | Connection pools, the read-only query path, the write path, Postgres error formatting, text-to-JSON value conversion. |
| `server.go` | Routes, auth, the metadata cache and every `/api/*` handler except the writing ones. |
| `write.go` | Read-write sources: console writes and the single-row insert, update and delete endpoints. |
| `ai.go` | Ask AI: builds the prompt and calls Claude (Anthropic API or hostit), with plain `net/http`. |
| `web/` | The embedded UI. See [frontend.md](frontend.md). |
| `packaging/` | systemd unit and maintainer scripts for the .deb/.rpm. See [packaging.md](packaging.md). |

## Config

`loadConfig` reads the JSON file, fills in defaults and validates sources (unique ids made of `[a-zA-Z0-9_-]`, a non-empty DSN). It writes the file back only when it had to generate something (the password or the secret, or the file did not exist), so a hand-written or read-only config is never rewritten. The session cookie is an HMAC of the password keyed by the secret, so changing either logs everyone out. `no_auth` skips the middleware entirely.

## Queries

Every database call goes through `db.query(ctx, src, sql, limit, args...)`:

1. The source's pool is created on first use (`pgxpool`, at most 4 connections, idle ones closed after 5 minutes). Connections set `default_transaction_read_only=on` and `application_name=dbexplore`.
2. A transaction is opened with `AccessMode: ReadOnly`, and `statement_timeout` is set for it with `set_config(..., true)`.
3. The statement runs with `QueryExecModeDescribeExec`, i.e. the extended protocol. Postgres refuses more than one statement in a Parse message, which is what stops `select 1; delete ...`. Because `set_config` already ran, `set transaction read write` (or `set_config('transaction_read_only', 'off', true)`) fails with "must be set before any query".
4. At most `limit` rows are read. If another row is available, `has_more` is set; the paged endpoints ask for `page_size + 1` rows in SQL for exactly that reason.
5. The transaction is always rolled back.

The free-SQL endpoint additionally only accepts statements starting with `SELECT`, `WITH`, `VALUES`, `TABLE`, `EXPLAIN` or `SHOW`, after skipping leading comments and parentheses (`stripLeading`). Paged ones (the first four) are wrapped as `select * from (<sql>\n) q limit .. offset ..`; the newline ends a trailing `--` comment. `EXPLAIN` and `SHOW` cannot be wrapped, so they return at most `max_rows` rows with `truncated` set instead of `has_more`. WHERE and ORDER BY text from the table bars is pasted into SQL as-is: that is the point of the feature, and the read-only transaction plus single-statement rule are what keep it safe.

None of this replaces a read-only database role (see the README), since some functions (`pg_terminate_backend`, file access for superusers) work in read-only transactions.

## Writes

Only sources with `read_write` write, and only through `db.write(ctx, src, sql, limit, check, args...)`, which refuses other sources itself. It acquires a pooled connection and hijacks it, opens a `ReadWrite` transaction (overriding the connection's `default_transaction_read_only`), sets `statement_timeout`, runs the statement, reads at most `limit` rows, calls `check` with the command tag, and commits. The connection is closed afterwards, so settings, temp tables or advisory locks the statement left behind never reach a later query. The result carries the command tag as `command`.

Console statements (`writeQuery`) take this path when they don't start like a read, or when the read failed with SQLSTATE 25006 (read-only transaction) or 0A000 "data-modifying statement" (a data-modifying `WITH` wrapped for paging). Statements matching `sessionSQL` (transaction control, `SET`, `RESET`, `DISCARD`) are refused. A console write drops the source's cached metadata, since it may have been DDL.

`/api/rows/{insert,update,delete}` build the statement from the cached column list: column names must exist and are quoted, values are passed as untyped text parameters so Postgres infers the column type and parses them like literals (an explicit `::type` cast would silently truncate `char(n)`). Updates and deletes must name exactly the primary key columns, and their `check` rolls back unless the command tag says one row. Insert and update return the row (`returning *`).

### Values

Results are requested in text format for every column (`QueryResultFormatsByOID{}`), so any type, including extension types, comes back as Postgres prints it. `textValue` then maps by type OID:

| Postgres type | JSON |
| --- | --- |
| `bool` | `true` / `false` |
| `int2`, `int4`, `oid` | number |
| `int8`, `numeric`, `float4`, `float8` | number, or a string if it has more than 15 significant digits or is `NaN`/`Infinity` (JavaScript numbers are doubles) |
| `json`, `jsonb` | the JSON value itself |
| everything else | string, as Postgres formats it |
| NULL | `null` |

### Errors

Postgres errors become 400 responses with the message, detail, hint and position, shown verbatim in the UI. A timeout becomes "query timed out". Anything else (the database unreachable) is a 502 and is logged.

## Metadata

The tree (`pg_class` + `pg_namespace`), columns (`pg_attribute` + the primary key from `pg_index`), foreign keys (`pg_constraint`, multi-column aware) and the AI schema summary (`information_schema.columns`) are cached in memory for 10 minutes. The tree's ⟳ button passes `refresh=1` to reload it. Relation names in catalog queries are passed as `$1::regclass` parameters, quoted with `qi`.

DDL is not cached. It is rebuilt from the catalog on each request: `CREATE VIEW` / `CREATE MATERIALIZED VIEW` with `pg_get_viewdef` for views, and for tables the columns (`pg_attrdef` defaults, identity and generated columns), `pg_get_constraintdef` for primary key, unique, foreign key, check and exclusion constraints, `pg_get_partkeydef`, and `pg_get_indexdef` for every index not already behind a constraint. It is for reading, not a pg_dump replacement.

## Ask AI

The backend is picked once at startup (`newAssistant`), and the startup log says which (`ai=anthropic|hostit|off`):

1. **Anthropic API**, if `ai.api_key` or `$ANTHROPIC_API_KEY` is set. One non-streaming Messages API request (`x-api-key`, `anthropic-version`), adaptive thinking, `ai.model` (default `claude-opus-5-5`) and `ai.base_url` for a gateway.
2. **hostit**, if dbexplore runs as a hostit app. Every hostit app container has hostit's container API on `127.0.0.1:2586`; `POST /api/container/assistant` answers `{system, prompt, max_tokens}` with `{text}` using the operator's Claude backend. The app is identified by its uid, so there is no key or token. The probe is `GET /api/container/assistant/models` with a 1s timeout; a 501 there means hostit has no backend configured, and Ask AI stays off. `ai.model` is only sent if set; otherwise hostit picks its default and falls through to the next model on a rate limit.
3. Otherwise Ask AI is off and hidden in the UI.

Both get the same request:

- a system prompt asking for exactly one read-only statement, followed by `ai.context` and the schema summary (`schema.table(column type, ...)` per line). For the Anthropic API it is marked for prompt caching, since it is the same for every request against a source;
- the user's request, the console's current SQL (so "add a filter on X" works) and the open table, if any.

Markdown fences are stripped from the reply. The frontend puts the SQL in the editor and runs it. Only the schema and the prompt leave the server, never row data. Errors from either backend (bad key, rate limit) are shown to the user verbatim.

## HTTP API

All endpoints except `/login`, `/login.html` and the icons need the session cookie; API calls without it get a 401, which the UI turns into a redirect to the login page.

| Endpoint | What |
| --- | --- |
| `POST /login` | Form field `password`. Sets the cookie. |
| `GET /api/sources` | `[{id, label, ai, read_write}]` |
| `GET /api/tree?src=&refresh=` | `{schemas: [{name, tables: [{name, kind, est}]}]}`. `kind` is the `relkind`, `est` the planner's row estimate (-1 if never analyzed). |
| `GET /api/columns?src=&schema=&table=` | `[{name, type, notnull, pk}]` |
| `GET /api/fks?src=&schema=&table=` | `[{name, cols, ref_schema, ref_table, ref_cols}]` |
| `GET /api/ddl?src=&schema=&table=` | `{ddl}` |
| `POST /api/table` | `{src, schema, table, where, order, page, page_size}` → result |
| `POST /api/count` | `{src, schema, table, where}` → `{count}` |
| `POST /api/query` | `{src, sql, page, page_size}` → result |
| `POST /api/assist` | `{src, prompt, current_sql, table}` → `{sql}` |
| `POST /api/rows/insert` | `{src, schema, table, values}` → result with the new row |
| `POST /api/rows/update` | `{src, schema, table, key, set}` → result with the updated row |
| `POST /api/rows/delete` | `{src, schema, table, key}` → result |

A result is `{columns, rows, has_more, truncated, ms, sql, command}`; `command` is the command tag of a committed write. Errors are `{error}` with a 4xx/5xx status. POST bodies must be sent as `application/json`, which a cross-site form cannot do without a CORS preflight.

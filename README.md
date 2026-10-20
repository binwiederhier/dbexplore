# dbexplore

A small, DataGrip-style explorer for PostgreSQL databases that runs in your browser. It's handy when you want to look around a database (a production replica, a staging DB, a reporting DB) without installing a desktop client, or want to give people a safe way to read data without handing out a SQL shell.

It is a single Go binary. The frontend is embedded and written in plain JavaScript with no build step. Every query runs in a read-only transaction with a statement timeout, unless you mark a database as read-write.

![A table tab with a WHERE filter, sorting, an exact row count and a few selected rows](docs/screenshots/table.png)

## Features

- **Schema tree** on the left: every configured database, then schemas, then tables and views with estimated row counts. A filter box on top narrows it down.
- **Table tabs**: clicking a table opens it in a tab with a paged grid.
  - WHERE and ORDER BY bars (Enter applies), click a column header to sort.
  - Σ counts the matching rows exactly, on demand.
  - DDL shows the table's columns with defaults, its constraints, partition key and indexes, or a view's definition.
  - Foreign key columns are links: hovering a value shows the referenced row in a card, clicking it opens the referenced table in a new tab, filtered to that row.
- **Rows**:
  - Click, Shift+click, Ctrl+click or drag to select rows, Ctrl+A selects the page.
  - Ctrl+C or ⧉ TSV copies the selection (or the page) as tab-separated values with a header line.
  - Double-click a row to see every field, with JSON pretty-printed, and step through rows with the arrow keys.
  - Each tab can switch between a table view and a cards view.
- **Query consoles**: free SQL with Ctrl+Enter, paged like a table. `SELECT`, `WITH`, `VALUES`, `TABLE`, `EXPLAIN` and `SHOW` are allowed.
- **Read-write databases** (opt-in per database with `read_write`): the console also runs `INSERT`, `UPDATE`, `DELETE` and DDL, each statement committed on its own. The row view gets Edit and Delete, and tables a + Row button. See [Read-write databases](#read-write-databases).
- **Ask AI** (optional): describe a query in words and Claude writes it from the database's schema. It shows up once you configure an Anthropic API key, or on its own when dbexplore runs as a [hostit](https://github.com/binwiederhier/hostit) app.
- Open tabs, filters, console SQL and the tree state are kept in the browser across reloads.

| Query console | Row view |
| --- | --- |
| ![Query console with a join and its paged result](docs/screenshots/console.png) | ![Row view with every field of a customer and its JSON pretty-printed](docs/screenshots/row.png) |
| **Cards view** | **DDL** |
| ![Cards view of the product table](docs/screenshots/cards.png) | ![DDL of a table with constraints and indexes](docs/screenshots/ddl.png) |

The screenshots show `testdata/demo.sql`; `make screenshots` retakes them.

## Install

### Debian / Ubuntu (.deb)

Download the `.deb` for your architecture from the [releases page](https://github.com/binwiederhier/dbexplore/releases) and install it:

```sh
sudo apt install ./dbexplore_*_amd64.deb
```

This installs `/usr/bin/dbexplore` and a systemd service that runs as the `dbexplore` system user, with its config in `/etc/dbexplore/config.json`. The service starts right away and generates the config on first start. Then:

```sh
sudoedit /etc/dbexplore/config.json        # add your databases, see Configuration
sudo systemctl restart dbexplore
sudo -u dbexplore dbexplore -config /etc/dbexplore/config.json -print-password
```

By default it only listens on `127.0.0.1:7879`. Set `listen` to `0.0.0.0:7879` to reach it from other machines, ideally behind a reverse proxy with TLS. Logs are in `journalctl -u dbexplore`. `apt remove dbexplore` keeps the config, `apt purge dbexplore` removes it and the user.

An `.rpm` with the same layout is on the releases page too.

### Binary

Download the archive for your platform (Linux or macOS, amd64 or arm64), put `dbexplore` on your PATH and run it:

```sh
tar xzf dbexplore_v*_linux_amd64.tar.gz
./dbexplore                 # creates ~/.config/dbexplore/config.json on first start
./dbexplore -print-password
```

### From source

Needs Go 1.25+.

```sh
git clone https://github.com/binwiederhier/dbexplore
cd dbexplore
make install    # builds, installs ~/.local/bin/dbexplore, enables + starts a systemd user service
make password   # prints the generated login password
```

Then open http://127.0.0.1:7879. To keep the user service running after you log out, enable lingering: `loginctl enable-linger $USER`. `make logs` follows the journal and `make uninstall` removes the binary and the unit (the config stays).

### hostit

dbexplore runs as a [hostit](https://github.com/binwiederhier/hostit) app: `"listen": "0.0.0.0:${PORT}"` takes the port hostit assigns, Ask AI uses hostit's assistant without an API key, and a source can take its database password from a hostit connection instead of the config:

```json
{ "id": "app", "name": "App DB", "dsn": "postgres://explorer@/app?sslmode=require", "hostit_connection": "app-db" }
```

The password is the connection's token, fetched for every new database connection, so a rotated credential is picked up without a restart. If the DSN has no host, it comes from the connection's note (`postgresql://host:port`). `deploy/ntfydb/` is a complete example, deployed with `make deploy-hostit HOSTIT_TOKEN=<app token>`; make the app private, since it serves real data.

## Configuration

The config file is `~/.config/dbexplore/config.json`, or `/etc/dbexplore/config.json` for the packaged service. Use `-config <path>` or `$DBEXPLORE_CONFIG` to point somewhere else. It is created on first start; restart after editing it.

```json
{
  "listen": "127.0.0.1:7879",
  "password": "generated-on-first-start",
  "secret": "generated-on-first-start",
  "sources": [
    { "id": "app", "name": "App DB (replica)", "dsn": "postgres://explorer:secret@db.internal:5432/app?sslmode=require" },
    { "id": "reports", "name": "Reporting", "dsn": "host=reports.internal dbname=reports user=explorer sslmode=verify-full" }
  ],
  "max_rows": 1000,
  "statement_timeout": "30s",
  "ai": {
    "api_key": "sk-ant-...",
    "context": "An online shop: customers place orders, orders have line items."
  }
}
```

| Key | Meaning |
| --- | --- |
| `listen` | Address to listen on. The default only accepts local connections. Environment variables are expanded (`0.0.0.0:${PORT}`). |
| `password` | Login password. Changing it logs out all sessions. |
| `secret` | HMAC key for the session cookie. Don't share it. |
| `no_auth` | `true` turns the login off. Only use this behind a reverse proxy that does its own authentication. |
| `sources` | The databases. `id` is used in URLs (letters, digits, `-`, `_`), `name` is shown in the tree, `dsn` is a [libpq connection string or URL](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING). `PGPASSWORD`, `~/.pgpass` and the other `PG*` variables work too. |
| `sources[].read_write` | `true` lets the console and the row editor change this database. Default `false`. See [Read-write databases](#read-write-databases). |
| `sources[].hostit_connection` | On hostit: the connection whose token is the database password (and whose note supplies a missing host). |
| `max_rows` | Largest page size, and the row cap for `EXPLAIN`/`SHOW` output. Default 1000. |
| `statement_timeout` | Per-query timeout, as a Go duration (`30s`, `2m`). Default `30s`. |
| `ai.api_key` | Anthropic API key for Ask AI. If empty, `$ANTHROPIC_API_KEY` is used. Without either, dbexplore uses hostit's assistant when it runs as a hostit app (no key needed), and hides Ask AI otherwise. |
| `ai.model` | Model id. Default `claude-opus-5-5` with an API key; on hostit, hostit's default model. |
| `ai.base_url` | Anthropic API base URL, for a gateway or proxy. Default is Anthropic's API. |
| `ai.context` | A sentence or two about what the data is. It goes into the prompt and makes the generated SQL noticeably better. |

For the packaged service, `/etc/default/dbexplore` is read as an environment file, so `ANTHROPIC_API_KEY` (or any `PG*` variable) can go there instead of into the config.

## Security

dbexplore is meant to be safe to point at a database with real data, but the database role you give it is what really decides what can be read. In order of importance:

1. **Use a dedicated, read-only role.** For example:

   ```sql
   create role explorer login password '...';
   grant pg_read_all_data to explorer;            -- Postgres 14+; or grant select on the schemas you want
   ```

   Point it at a replica if you can. Don't use a superuser: superusers can read server files and run functions that read-only transactions do not block.
2. **Every query runs in a `READ ONLY` transaction** (for read-write sources, see below), with `default_transaction_read_only=on` on the connection and the configured `statement_timeout`. Writes, DDL, `nextval()` and so on fail in the database.
3. **One statement per request.** Queries use Postgres' extended protocol, which refuses `select 1; delete ...`. Free SQL must start with `SELECT`, `WITH`, `VALUES`, `TABLE`, `EXPLAIN` or `SHOW` (after any comments).
4. **Login.** A single password with a signed, HttpOnly session cookie. Put dbexplore behind TLS if it listens beyond localhost. The cookie is marked `Secure` when the request came over TLS, or through a proxy that sets `X-Forwarded-Proto: https`.

### Read-write databases

A source with `"read_write": true` is shown with an RW badge, and the top bar says READ-WRITE while one of its tabs is open. Browsing, counting, hover cards and DDL still run read-only. What changes:

- **Console.** Reads run read-only and paged as before. Any other statement runs in a read-write transaction that is committed right away, and the result shows the command tag (`UPDATE 3`) and any `RETURNING` rows (up to `max_rows`). A read that turns out to write, like a data-modifying `WITH` or `EXPLAIN ANALYZE` on a `DELETE`, fails read-only first and then runs as a write. `BEGIN`, `COMMIT`, `ROLLBACK`, `SAVEPOINT`, `SET`, `RESET` and `DISCARD` are refused, since every statement is its own transaction. The browser asks before `DROP`, `TRUNCATE`, and `UPDATE` or `DELETE` without `WHERE`. Ask AI only fills in the editor here, it does not run what it wrote.
- **Rows.** In a table with a primary key, the row view (double-click a row) has Edit and Delete, and the toolbar has + Row. Values are typed as text in SQL literal syntax (`true`, `{a,b}`, `{"k": 1}`) and parsed by the database as the column type; NULL is a toggle. Only changed fields are sent, empty fields of a new row get their default. Updates and deletes find the row by its primary key and are rolled back unless exactly one row matched.

A write's database connection is closed afterwards instead of going back to the pool, so whatever session state it set is gone. The database role still decides what can actually be written: give it only the grants it needs, for example `grant pg_read_all_data, pg_write_all_data to editor` for data but no DDL.

With Ask AI enabled, the schema (table and column names and types) and the user's prompt are sent to the Anthropic API, or to hostit, which passes them to its Claude backend. Row data is not.

## Development

```sh
make build      # ./dbexplore
make test       # go vet, go test, node --check web/app.js
make demo-db    # throwaway Postgres in Docker on 127.0.0.1:55432 with testdata/demo.sql, roles explorer and editor
make test-db    # make test against the demo database, including the database tests
make deb        # snapshot tarballs, .deb and .rpm into dist/ (needs goreleaser)
```

`testdata/demo.sql` is a small online shop (foreign keys, a composite key, JSON, a view, a materialized view, a partitioned table, reserved words and odd identifiers as names). It is what the database tests expect and what the screenshots show. To try the UI against it, add it as a source:

```json
{ "id": "demo", "name": "Demo shop", "dsn": "postgres://explorer:explorer@127.0.0.1:55432/postgres?sslmode=disable" }
```

For read-write editing, add the same database as the `editor` role with `"read_write": true`:

```json
{ "id": "demo-rw", "name": "Demo shop (read-write)", "dsn": "postgres://editor:editor@127.0.0.1:55432/postgres?sslmode=disable", "read_write": true }
```

The database tests run when `DBEXPLORE_TEST_DSN` is set; the ones that need the demo data skip if it is not loaded. They only read. The write tests also need `DBEXPLORE_TEST_WRITE_DSN` (the `editor` role) and remove the rows they add. `make test-db` sets both.

How it works is in [docs/](docs/README.md).

## License

[Apache License 2.0](LICENSE)

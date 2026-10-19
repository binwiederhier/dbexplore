# dbexplore

A small, read-only, DataGrip-style explorer for PostgreSQL databases that runs in your browser. It's handy when you want to look around a database (a production replica, a staging DB, a reporting DB) without installing a desktop client, or want to give people a safe way to read data without handing out a SQL shell.

It is a single Go binary. The frontend is embedded and written in plain JavaScript with no build step. Every query runs in a read-only transaction with a statement timeout.

## Features

- **Schema tree** on the left: every configured database, then schemas, then tables and views with estimated row counts. A filter box on top narrows it down.
- **Table tabs**: clicking a table opens it in a tab with a paged grid.
  - WHERE and ORDER BY bars (Enter applies), click a column header to sort.
  - Σ counts the matching rows exactly, on demand.
  - DDL shows the table's columns, primary key and indexes.
  - Foreign key columns are links: clicking a value opens the referenced table in a new tab, filtered to that row.
- **Rows**:
  - Click, Shift+click, Ctrl+click or drag to select rows, Ctrl+A selects the page.
  - Ctrl+C or ⧉ TSV copies the selection (or the page) as tab-separated values with a header line.
  - Double-click a row to see every field, with JSON pretty-printed, and step through rows with the arrow keys.
  - Each tab can switch between a table view and a cards view.
- **Query consoles**: free SQL with Ctrl+Enter, paged like a table. `SELECT`, `WITH`, `VALUES`, `TABLE`, `EXPLAIN` and `SHOW` are allowed.
- **Ask AI** (optional): describe a query in words and Claude writes it from the database's schema. It shows up once you configure an Anthropic API key.
- Open tabs, filters, console SQL and the tree state are kept in the browser across reloads.

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
| `listen` | Address to listen on. The default only accepts local connections. |
| `password` | Login password. Changing it logs out all sessions. |
| `secret` | HMAC key for the session cookie. Don't share it. |
| `no_auth` | `true` turns the login off. Only use this behind a reverse proxy that does its own authentication. |
| `sources` | The databases. `id` is used in URLs (letters, digits, `-`, `_`), `name` is shown in the tree, `dsn` is a [libpq connection string or URL](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING). `PGPASSWORD`, `~/.pgpass` and the other `PG*` variables work too. |
| `max_rows` | Largest page size, and the row cap for `EXPLAIN`/`SHOW` output. Default 1000. |
| `statement_timeout` | Per-query timeout, as a Go duration (`30s`, `2m`). Default `30s`. |
| `ai.api_key` | Anthropic API key for Ask AI. If empty, `$ANTHROPIC_API_KEY` is used. Without either, Ask AI is hidden. |
| `ai.model` | Model id. Default `claude-opus-5-5`. |
| `ai.base_url` | API base URL, for a gateway or proxy. Default is Anthropic's API. |
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
2. **Every query runs in a `READ ONLY` transaction**, with `default_transaction_read_only=on` on the connection and the configured `statement_timeout`. Writes, DDL, `nextval()` and so on fail in the database.
3. **One statement per request.** Queries use Postgres' extended protocol, which refuses `select 1; delete ...`. Free SQL must start with `SELECT`, `WITH`, `VALUES`, `TABLE`, `EXPLAIN` or `SHOW`.
4. **Login.** A single password with a signed, HttpOnly session cookie. Put dbexplore behind TLS if it listens beyond localhost.

With Ask AI enabled, the schema (table and column names and types) and the user's prompt are sent to the configured API. Row data is not.

## Development

```sh
make build      # ./dbexplore
make test       # go vet, go test, node --check web/app.js
make deb        # snapshot tarballs, .deb and .rpm into dist/ (needs goreleaser)
```

`go test` also runs the database tests when `DBEXPLORE_TEST_DSN` points at a Postgres database, e.g. `DBEXPLORE_TEST_DSN=postgres://postgres@localhost/postgres?sslmode=disable make test`. The tests only read.

How it works is in [docs/](docs/README.md).

## License

[Apache License 2.0](LICENSE)

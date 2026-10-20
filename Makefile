BIN := $(HOME)/.local/bin/dbexplore
UNIT := $(HOME)/.config/systemd/user/dbexplore.service

.PHONY: build test demo-db test-db screenshots deploy-hostit check-deps install uninstall restart logs password deb release clean

build:
	go build -o dbexplore .

test:
	go vet ./...
	go test ./...
	node --check web/app.js

DEMO_CONTAINER := dbexplore-demo
DEMO_DSN := postgres://explorer:explorer@127.0.0.1:55432/postgres?sslmode=disable
DEMO_WRITE_DSN := postgres://editor:editor@127.0.0.1:55432/postgres?sslmode=disable

# Starts (or restarts) a throwaway Postgres in Docker with testdata/demo.sql, a read-only
# "explorer" role as the README recommends for real databases, and an "editor" role that may
# write data, for read_write sources.
demo-db:
	-docker rm -f $(DEMO_CONTAINER) >/dev/null 2>&1
	docker run -d --name $(DEMO_CONTAINER) -e POSTGRES_PASSWORD=postgres -p 127.0.0.1:55432:5432 postgres:17 >/dev/null
	@until docker exec $(DEMO_CONTAINER) pg_isready -U postgres -h 127.0.0.1 >/dev/null 2>&1; do sleep 1; done
	docker exec -i $(DEMO_CONTAINER) psql -q -U postgres -v ON_ERROR_STOP=1 < testdata/demo.sql
	docker exec $(DEMO_CONTAINER) psql -q -U postgres -c "create role explorer login password 'explorer'; grant pg_read_all_data to explorer;"
	docker exec $(DEMO_CONTAINER) psql -q -U postgres -c "create role editor login password 'editor'; grant pg_read_all_data, pg_write_all_data to editor;"
	@echo "demo database: $(DEMO_DSN) (read-write: $(DEMO_WRITE_DSN))"

test-db:
	DBEXPLORE_TEST_DSN="$(DEMO_DSN)" DBEXPLORE_TEST_WRITE_DSN="$(DEMO_WRITE_DSN)" $(MAKE) test

# Retakes docs/screenshots/*.png against the demo database (make demo-db). Needs node.
screenshots: build
	cd scripts && npm install --no-save --silent playwright@1.61.1
	node scripts/screenshots.mjs

# Deploys to a hostit app: builds linux/amd64 locally (a Go build does not fit an app's CPU
# budget), uploads it to bin/ with hostit.yml from HOSTIT_DIR, and restarts the app. config.json
# is only uploaded if the app has none (or with HOSTIT_CONFIG=1), since dbexplore writes its
# generated password into it on first start. HOSTIT_TOKEN is the app's token from the hostit UI.
HOSTIT_APP ?= ntfydb
HOSTIT_URL ?= https://apps.heckel.haus
HOSTIT_DIR ?= deploy/$(HOSTIT_APP)
HOSTIT_API := $(HOSTIT_URL)/api/apps/$(HOSTIT_APP)
HOSTIT_AUTH = Authorization: Bearer $(HOSTIT_TOKEN)

deploy-hostit:
	@test -n "$(HOSTIT_TOKEN)" || { echo "HOSTIT_TOKEN is not set (the app token of $(HOSTIT_APP))"; exit 1; }
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=$$(git describe --tags --always --dirty)" -o dist/dbexplore-linux-amd64 .
	curl -sf -H "$(HOSTIT_AUTH)" -X PUT "$(HOSTIT_API)/files/bin/dbexplore?mode=755" --data-binary @dist/dbexplore-linux-amd64 >/dev/null
	curl -sf -H "$(HOSTIT_AUTH)" -X PUT "$(HOSTIT_API)/files/hostit.yml" --data-binary @$(HOSTIT_DIR)/hostit.yml >/dev/null
	@if [ "$(HOSTIT_CONFIG)" = 1 ] || ! curl -sf -o /dev/null -H "$(HOSTIT_AUTH)" "$(HOSTIT_API)/files/config.json"; then \
		echo "uploading config.json"; \
		curl -sf -H "$(HOSTIT_AUTH)" -X PUT "$(HOSTIT_API)/files/config.json" --data-binary @$(HOSTIT_DIR)/config.json >/dev/null; \
	fi
	curl -sf -H "$(HOSTIT_AUTH)" -X POST "$(HOSTIT_API)/deploy"; echo

check-deps:
	@command -v go >/dev/null || { echo "go is not installed (need Go 1.25+)"; exit 1; }

install: check-deps build
	mkdir -p $(dir $(BIN)) $(dir $(UNIT))
	install -m 755 dbexplore $(BIN)
	cp dbexplore.service $(UNIT)
	systemctl --user daemon-reload
	systemctl --user enable dbexplore
	systemctl --user restart dbexplore
	@sleep 1
	@echo "dbexplore is running: $$(grep -o '"listen": *"[^"]*"' $(HOME)/.config/dbexplore/config.json)"
	@echo "password: $$($(BIN) -print-password)"

uninstall:
	-systemctl --user disable --now dbexplore
	rm -f $(BIN) $(UNIT)
	systemctl --user daemon-reload

restart: install

logs:
	journalctl --user -u dbexplore -f

password:
	@$(BIN) -print-password

# Builds the tarballs, .deb and .rpm into dist/ without publishing anything.
deb:
	goreleaser release --snapshot --clean

# Publishes a draft GitHub release for the current tag. Needs GITHUB_TOKEN.
release:
	goreleaser release --clean

clean:
	rm -rf dbexplore dist

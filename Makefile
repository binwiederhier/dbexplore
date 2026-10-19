BIN := $(HOME)/.local/bin/dbexplore
UNIT := $(HOME)/.config/systemd/user/dbexplore.service

.PHONY: build test check-deps install uninstall restart logs password deb release clean

build:
	go build -o dbexplore .

test:
	go vet ./...
	go test ./...
	node --check web/app.js

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

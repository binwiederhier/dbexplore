# Packaging

Releases are built with [GoReleaser](https://goreleaser.com) from `.goreleaser.yaml`:

- static binaries (`CGO_ENABLED=0`) for linux and darwin, amd64 and arm64, with `main.version` set from the tag;
- `dbexplore_<tag>_<os>_<arch>.tar.gz` archives with the binary, LICENSE, README and the systemd user unit;
- `.deb` and `.rpm` packages via nfpm;
- `checksums.txt`.

`make deb` builds all of it into `dist/` as a snapshot without publishing. `make release` builds the current git tag and creates a draft GitHub release (needs `GITHUB_TOKEN`).

## .deb / .rpm layout

| Path | What |
| --- | --- |
| `/usr/bin/dbexplore` | The binary. |
| `/lib/systemd/system/dbexplore.service` | System service, runs as `dbexplore` with `-config /etc/dbexplore/config.json`. |
| `/etc/dbexplore/` | Config directory, mode 0700, owned by `dbexplore`. `config.json` is generated on first start. |
| `/etc/default/dbexplore` | Environment file for the service (`ANTHROPIC_API_KEY`, `PG*`). A conffile, kept on upgrade. |

Maintainer scripts:

- `postinstall.sh` creates the `dbexplore` system user, fixes ownership of `/etc/dbexplore`, then enables and (re)starts the service.
- `preremove.sh` stops and disables the service on removal (not on upgrade).
- `postremove.sh` deletes `/etc/dbexplore` and the user on purge only.

The service is hardened with `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices` and `NoNewPrivileges`; the only writable path is `/etc/dbexplore`, which it needs to generate the config on first start.

## User service

`dbexplore.service` at the repo root is the systemd user unit that `make install` copies to `~/.config/systemd/user/`. It runs `~/.local/bin/dbexplore` with the default config path.

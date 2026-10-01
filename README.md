# dbpod

[https://dbpod.io](https://dbpod.io)

dbpod is a lightweight, cross-platform, project-local database management CLI.

It manages install-free database engine binaries (like images), runs them as
detached background processes (like containers, no daemon), and stores data in
project-local volumes under `./.dbpod/`. A `dbpod.yaml` in the project root
reproduces the exact database environment with one command.

Think "Docker for databases" without the daemon: engines are cached once,
instances start in seconds, and every project owns its data.

## Features

- **Engine management** — download, verify (md5/sha256) and cache engine
  distributions; list versions with release-series folding and LTS markers.
  Built-in engines: **MySQL** and **PostgreSQL**. Extra engines (e.g.
  MariaDB) can be mounted via config-declared manifests
  (`dbpod registry`, `engines.d`).
- **Instance lifecycle** — run engines as detached background processes
  supervised by a per-instance monitor (conmon-style). Start, stop, restart,
  inspect, tail logs; `--rm` instances clean up after themselves.
- **Project workflow** — `dbpod.yaml` declares the engine, version, port and
  optional `init-sql`; `dbpod project up` initializes the datadir and imports
  the seed SQL on first run. Commit the yaml, gitignore `.dbpod/`.
- **`dbpod exec`** — run any binary shipped with an engine's distribution
  (`mysql`, `mysqldump`, `psql`, ...). For an instance target the client is
  pre-wired with the connection parameters.
- **Network friendly** — configurable proxy, published checksums verified on
  every download, audit log of all fetches.

## Install

One-line install:

**linux / macos / wsl**

```sh
curl -fsSL https://dbpod.io/install.sh | sh
```

**windows PowerShell**

```powershell
irm https://dbpod.io/install.ps1 | iex
```

**windows cmd**

```bat
curl -fsSL https://dbpod.io/install.bat -o "%TEMP%\dbpod-install.bat" && call "%TEMP%\dbpod-install.bat"
```

Or grab a binary from [Releases](https://github.com/dbpod-io/dbpod/releases)
(`dbpod-<os>-<arch>.tar.gz` / `.zip`), or build from source:

```sh
go install github.com/dbpod-io/dbpod@latest
```

## Quick start

Project-local workflow (recommended):

```sh
dbpod project init --engine mysql@8.0 --port 3306
dbpod project up                                   # install, init, start
dbpod project exec mysql -e "SELECT VERSION()"
dbpod project logs -f
dbpod project down                                 # stop
dbpod project clean                                # stop + wipe local state
```

Ad-hoc instances:

```sh
dbpod engine install mysql@8.0.46
dbpod run --engine=mysql@8.0.46 --name=app-db --port=3307 --detach
dbpod ps
dbpod exec app-db mysql -e "SHOW DATABASES"
dbpod kill app-db
dbpod rm app-db          # remove record + datadir
```

Engine cache management:

```sh
dbpod engine ls                 # installed versions + release series
dbpod engine ls --all           # every known version
dbpod engine rm mysql@8.0.46
```

## Version

```sh
$ dbpod version          # same output: dbpod --version
dbpod version v0.1.0 (commit bd46b2a)
go1.27.0 darwin/arm64
```

Releases inject the version and commit via `-ldflags`
(`github.com/dbpod-io/dbpod/cmd.Version` / `.Commit`).

## Development

```sh
go build -o dbpod .   # build the CLI
go test ./...         # run the test suite
```

Release automation: pushing a `v*` tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which runs
[goreleaser](https://goreleaser.com) (`.goreleaser.yaml`) to publish binaries
for darwin/linux/windows on amd64/arm64.

## Contributing

Found a bug or have an idea? [Open an issue](https://github.com/dbpod-io/dbpod/issues) —
reports and pull requests are welcome.

## License

[Apache License 2.0](LICENSE)

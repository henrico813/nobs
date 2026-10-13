# nobs

nobs is a small broker and CLI for one Obsidian vault. Run `nobs service` where the vault and `ob` are available, then use the CLI to read, search, change, and sync notes through that broker.

Agents can pass the full path that `nobs find` prints straight to `nobs read`. Other commands that take a note path need it relative to the vault, such as `Projects/Plan.md`, and reject absolute paths.

## Install

Each release from `v0.3.1` on has Linux binaries for amd64 and arm64 and a `checksums.txt`. Download the binary for your computer, check it, and put it on your `PATH`:

```sh
VERSION=v0.3.2
ARCH=amd64   # or arm64
curl -fLO "https://github.com/henrico813/nobs/releases/download/$VERSION/nobs_linux_$ARCH"
curl -fLO "https://github.com/henrico813/nobs/releases/download/$VERSION/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
install -D -m 755 "nobs_linux_$ARCH" ~/.local/bin/nobs
```

With Go installed, `go install` builds the same release, and works for earlier tags too:

```sh
go install github.com/henrico813/nobs@v0.3.2
```

From `v0.3.2` on, `go install` needs Go 1.27.2, or Go 1.21 or later with the default `GOTOOLCHAIN=auto`, which downloads it.

Both kinds of release know their version, so they start a broker of that version (see "Brokers on a computer"). A local `go build` of an untagged commit does not, and needs `NOBS_VERSION`.

## Releases

Pushing a tag such as `v1.2.3` runs `.github/workflows/release.yml`. Only plain `vX.Y.Z` tags run it, the only form `nobs` accepts as a release version; a tag such as `v1.2.3-rc1` publishes nothing. The job stops unless the tagged commit is on `main`. It runs `go test ./...`, builds both binaries with the tag stamped in, checks that Go recorded the tag and no local changes, and publishes them with `checksums.txt` as a GitHub release. Pushing a tag publishes a release, so push one only when `main` is ready. A binary built by hand the same way also knows its version. Build it from the tagged commit, because the broker image is built from the tag; a stamp on other code starts an image that does not match the binary:

```sh
CGO_ENABLED=0 go build -ldflags "-X main.version=v0.3.2" -o nobs .
```

The `go` line in `go.mod` and the `golang` image in the `Dockerfile` name the same Go release, and the job stops when they differ, so change both together.

## Broker settings

- `NOBS_LISTEN` sets the address for `nobs service`. It defaults to `:8080`.
- Set `NOBS_VAULT_IS_MOUNTED=true` when the vault is on a mount that might be missing. The broker then refuses health, vault, and sync requests, including `sync-now`, until `.obsidian` exists, so only use it for a vault that is already enrolled and synced. It is off when unset.
- `OBSIDIAN_VAULT_DIR` sets the vault folder. It defaults to `/vault`. Use an absolute path; with a relative one, `nobs read` cannot take the paths `nobs find` prints.

## Choosing a broker

Commands other than `service` and `vault` send their work to one broker. The first of these that is set wins:

1. `--vault NAME` before the command, as in `nobs --vault work find REF`, uses that vault's port on `127.0.0.1`. An unknown name is an error.
2. `NOBS_BROKER_URL`, such as `http://127.0.0.1:8080`. Without `--vault`, the settings file is not read, so a container with only this variable never depends on it.
3. The default vault in the settings file.

With none of them, commands fail and say to run `nobs vault set`.

## Vaults on a computer

`nobs vault` keeps the vaults on this computer in `$XDG_CONFIG_HOME/nobs/config.json`, or `~/.config/nobs/config.json` when `XDG_CONFIG_HOME` is unset or not an absolute path. Change it only with these commands:

```sh
nobs vault set NAME PATH     # add a vault or change its folder, and start its broker
nobs vault rm NAME           # stop and remove its broker, and remove the vault
nobs vault list              # JSON list of name, path, port and running
nobs vault path [NAME]       # the vault's folder; the default vault without NAME
nobs vault default [NAME]    # show or change the default vault
```

- Names start with a lowercase letter and use only lowercase letters, digits and hyphens.
- `PATH` must be absolute and contain `.obsidian/`. It is stored as given, without resolving links.
- `set` gives a new vault the lowest port from 8781 up that no other vault uses and nothing on `127.0.0.1` listens on. Changing a vault's folder keeps its port, and a port freed by `rm` is used again.
- When there is no default, the vault being set becomes the default. Removing the default vault leaves no default.
- Commands refuse a settings file with unknown keys, bad names, relative paths, shared ports or a default that is not listed.

## Brokers on a computer

Each vault gets one broker container, run with Docker Compose as project `nobs-NAME`. The Compose file is built into `nobs`. `set` runs `docker compose up -d` and `rm` runs `docker compose down`; the settings file changes only after Docker succeeds. Docker's output goes to stderr.

- The broker listens on `127.0.0.1` only, at the vault's port. It has no password.
- The vault is mounted at the same path it has on the computer, because `ob` finds a vault's sync setup by its folder path. `ob`'s settings folder, `obsidian-headless` next to the `nobs` config folder, is shared with every broker.
- Docker never creates a missing vault folder. If the drive is not mounted, the broker does not start.
- The broker runs as the user who ran `nobs`, so new files belong to them.
- `running` in `vault list` is true when the broker answers on its port.

When a command uses a vault's broker (through `--vault` or the default vault) and cannot connect, `nobs` runs `docker compose up -d` once, waits up to 10 seconds for the broker, and sends the request again. This starts a broker that could not start at boot because its drive was not mounted yet. A broker from `NOBS_BROKER_URL` is never started.

The broker image is `nobs:VERSION`, built from this repository's `VERSION` tag the first time it is needed, which can take a few minutes. VERSION is `NOBS_VERSION` when it is set. Otherwise it is the version of the `nobs` you run: the tag stamped into a release binary, or the version `go install` records, so the `v0.3.1` release binary and `go install github.com/henrico813/nobs@v0.3.1` both run `nobs:v0.3.1`. A build that is not a release, such as `go build` on an untagged commit, needs `NOBS_VERSION`. To try a local change, build the image and name it:

```sh
docker build -t nobs:dev .
export NOBS_VERSION=dev
nobs vault set NAME PATH
```

Every `nobs` command that may start a broker, including later ones, needs the same `NOBS_VERSION`. Run `nobs vault set` again after changing versions to replace the broker.

`/livez`, `/configz`, `find`, `search`, `rg` and `sync-status` create no files in the vault. Only `nobs health` (through `/readyz`) and `apply-patch` briefly create `.nobs-write-probe` to check that the vault is writable.

## Finding notes

Agents that know a note's issue code, file name or title, but not its path, use `nobs find REF` to get the note's full path. Like other commands, it prints indented JSON; these results are shown on one line each:

```json
{"status": "found", "path": "/vault/Projects/ABC-110 Move config.md"}
{"status": "ambiguous", "matches": ["/vault/A/Plan.md", "/vault/B/Plan.md"]}
{"status": "not_found"}
```

`--json` is accepted and ignored, since the output is always JSON. Errors, such as an unmounted vault, go to stderr with a nonzero exit code. Agent prompts outside this repository parse `status`, `path` and `matches`, so do not rename them.

It tries three rules in order and stops at the first that matches: an exact file name or vault-relative path, with or without `.md`; a case-insensitive prefix that ends at a space, hyphen, or underscore, so `ABC-04` does not match `ABC-042`; then a title that ignores case, punctuation, and a leading issue code. The last two skip `.backup-` notes. All three skip hidden folders such as `.trash`, notes that are links, and files that are not `.md`.

`find` and the other read commands (`search`, `rg`, `read`, `today` and `daily`) wait up to 150 seconds, because a running `sync-now` holds the vault for up to about 2 minutes. `find` writes nothing to the vault.

## License

nobs is licensed under the MIT license. See [LICENSE](LICENSE).

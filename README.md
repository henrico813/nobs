# nobs

nobs is a small broker and CLI for one Obsidian vault. Run `nobs service` where the vault and `ob` are available, then use the CLI to read, search, change, and sync notes through that broker.

Agents can pass the full path that `nobs find` prints straight to `nobs read`. Other commands that take a note path need it relative to the vault, such as `Projects/Plan.md`, and reject absolute paths.

## Build and install

Build a local binary:

```sh
go build -o nobs .
```

Install a tagged release:

```sh
go install github.com/henrico813/nobs@v0.2.0
```

Build the broker image:

```sh
docker build -t nobs:v0.2.0 .
```

## Broker settings

- `NOBS_LISTEN` sets the address for `nobs service`. It defaults to `:8080`.
- Set `NOBS_VAULT_IS_MOUNTED=true` when the vault is on a mount that might be missing. The broker then refuses health, vault, and sync requests, including `sync-now`, until `.obsidian` exists, so only use it for a vault that is already enrolled and synced. It is off when unset.
- `OBSIDIAN_VAULT_DIR` sets the vault folder. It defaults to `/vault`. Use an absolute path; with a relative one, `nobs read` cannot take the paths `nobs find` prints.

`NOBS_BROKER_URL` sets the broker address used by CLI commands, such as `http://127.0.0.1:8080`; without it, CLI commands fail.

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

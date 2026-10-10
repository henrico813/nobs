# nobs

nobs is a small broker and CLI for one Obsidian vault. Run `nobs service` where the vault and `ob` are available, then use the CLI to read, search, change, and sync notes through that broker.

## Build and install

Build a local binary:

```sh
go build -o nobs .
```

Install a tagged release:

```sh
go install github.com/henrico813/nobs@v0.1.0
```

Build the broker image:

```sh
docker build -t nobs:v0.1.0 .
```

## Broker settings

- `NOBS_LISTEN` sets the address for `nobs service`. It defaults to `:8080`.
- Set `NOBS_VAULT_IS_MOUNTED=true` when the vault is on a mount that might be missing. The broker then refuses health, vault, and sync requests, including `sync-now`, until `.obsidian` exists, so only use it for a vault that is already enrolled and synced. It is off when unset.
- `OBSIDIAN_VAULT_DIR` sets the vault folder. It defaults to `/vault`.

`NOBS_BROKER_URL` sets the broker address used by CLI commands, such as `http://127.0.0.1:8080`; without it, CLI commands fail.

## License

nobs is licensed under the MIT license. See [LICENSE](LICENSE).

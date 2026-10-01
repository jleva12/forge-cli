# Managing APIs

Each registered API is a profile: a spec plus its own server, filters, login, environment variable prefix and trusted hosts.

## Add

```sh
forge add <name> <spec-file-or-url> [flags]
forge add --name <name> --spec <spec-file-or-url> [flags]
```

```sh
forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
forge add billing ./specs/billing.yaml --server https://billing.internal.example.com --read-only
forge add stripe ./stripe.json --include-group customers --include-group charges --use
```

| Flag | What it does |
|---|---|
| `--server <url>` | Base URL to call, overriding the spec's `servers`. Needed when a spec loaded from a local file has no full server URL. Specs loaded from a URL default to that URL's host. |
| `--use` | Make this the active API |
| `--force` | Replace an existing API with the same name. Its stored login is deleted if the spec, server or trusted hosts change. Ask the user first. |
| `--include-group <g>` / `--exclude-group <g>` | Only expose, or hide, a command group (repeatable) |
| `--include-tag <t>` / `--exclude-tag <t>` | Only expose, or hide, operations with a tag (repeatable) |
| `--include-path <glob>` / `--exclude-path <glob>` | Filter by path, e.g. `/v1/**` (repeatable) |
| `--exclude-operation <id>` | Hide an operation by `operationId` (repeatable) |
| `--exclude-deprecated` | Hide deprecated operations |
| `--read-only` | Only expose GET, HEAD and OPTIONS operations |
| `--trust-host <host>` | Extra host allowed to receive credentials (repeatable) |
| `--env-prefix <PREFIX>` | Environment variable prefix (default: the name, upper-cased) |
| `--no-check` | Save without loading the spec |
| `--file <apis.toml>` | Register every API in a TOML file |

- **Groups or tags:** prefer group filters. Groups are the names shown by `forge commands` and work for every spec. Tag filters only work when the spec uses tags. If the filters hide every operation, `forge add` says so and lists the groups to choose from.
- **Path globs:** `*` matches within one path segment, `**` matches across segments, and `/admin/**` also matches `/admin`.
- **Read-only profiles for agents:** a separate profile such as `forge add billing-agent <spec> --read-only` keeps write commands out of reach.

After adding an API, check whether it needs a login with `forge --api <name> auth status`.

## Switch, list and remove

```sh
forge apis                # list registered APIs (● = active)
forge use                 # show the active API, its source and login state
forge use <name>          # make <name> the active API (persistent; ask the user first)
forge remove <name>...    # unregister and delete the stored login (alias: rm; ask the user first)
```

To run commands against an API without switching:

```sh
forge --api <name> <command> ...           # one command
FORGE_API=<name> forge <command> ...       # every command in this environment
forge --spec ./draft.yaml <command> ...    # an unregistered spec, ad hoc
```

## Registry file

APIs are stored in `~/.config/forge/apis.toml`. `forge apis --path` prints the exact path. It never contains secrets.

```toml
active = "petstore"

[apis.petstore]
  spec = "https://petstore3.swagger.io/api/v3/openapi.json"

[apis.billing]
  spec = "/Users/me/specs/billing.yaml"
  server = "https://billing.internal.example.com"
  trusted_hosts = ["billing-staging.internal.example.com"]
  exclude_groups = ["admin"]
  read_only = true
```

Keys: `spec`, `server`, `trusted_hosts`, `env_prefix`, `include_groups`, `exclude_groups`, `include_tags`, `exclude_tags`, `include_paths`, `exclude_paths`, `exclude_operations`, `exclude_deprecated`, `read_only`. Prefer `forge add ... --force` over editing the file by hand.

## Share with a team

```sh
forge apis --toml > team-apis.toml    # export (no secrets)
forge add --file team-apis.toml       # import; existing names are skipped unless --force
```

Relative spec paths in the file are resolved against the file's own location.

## Problems

| Message | Fix |
|---|---|
| `No API is active yet` | `forge add <name> <spec>`, or `forge use <name>` if it's already registered |
| `no API server URL ... relative` | Re-add with `--server https://... --force` |
| `the filters hide every operation` | Use `--include-group` with a group from the warning |
| `would share environment variables` | Two names map to the same prefix. Pick another name or set `--env-prefix`. |
| `loading <api> ...: unexpected status`, or slow startup | A spec registered by URL is downloaded on every run. Download it once (`curl -o spec.json <url>`) and re-add the local file with `--force`. |

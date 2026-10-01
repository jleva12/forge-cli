# forge

**forge turns any OpenAPI spec into a command-line tool.** Point it at an OpenAPI 3.x or Swagger 2.0 spec (JSON or YAML, file or URL) and every endpoint becomes a command with typed flags, help text, shell completion and authentication. No code generation is involved.

```sh
forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
forge pet find-pets-by-status --status available
forge pet get-pet-by-id --pet-id 10 -o yaml
```

- **Many APIs, one tool.** Register APIs under names and switch between them like profiles.
- **Typed commands:** flags are validated against the spec (types, required fields, allowed values) and shell-completed.
- **Secure auth:** tokens, API keys, basic auth and OAuth2 (browser, device code, client credentials) are stored in your OS keychain and never sent to hosts the API doesn't trust.
- **Built for AI agents:** a command index, JSON tool definitions (Anthropic and OpenAI formats), a JSON `call` interface and a ready-made [agent skill](#agent-skill).
- **A Go framework too:** build a dedicated CLI for your own API with filters, renames and better descriptions ([Building your own CLI](#building-your-own-cli)).

---

## Contents

1. [Install](#install)
2. [Quick start](#quick-start)
3. [How it works](#how-it-works)
4. [Managing APIs (profiles)](#managing-apis-profiles)
5. [Finding commands](#finding-commands)
6. [Running commands](#running-commands)
7. [Authentication](#authentication)
8. [Using forge with AI agents](#using-forge-with-ai-agents)
9. [Configuration reference](#configuration-reference)
10. [Troubleshooting](#troubleshooting)
11. [Building your own CLI](#building-your-own-cli)
12. [Development](#development)

---

## Install

Requires Go 1.27 or newer.

```sh
git clone <this repo> forge-cli && cd forge-cli
go install ./cmd/forge
```

If your Go is older, for example Homebrew's, `go install` stops with `go.mod requires go >= 1.27 (running go 1.23.4; GOTOOLCHAIN=local)`. Let Go download the version it needs:

```sh
GOTOOLCHAIN=auto go install ./cmd/forge
```

This puts a `forge` binary in `$(go env GOPATH)/bin` (usually `~/go/bin`). Make sure that directory is on your `PATH`:

```sh
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
forge --help
```

**Shell completion** covers commands, flags, allowed values and API names:

```sh
forge completion install
```

This detects your shell (bash, zsh or fish) from `$SHELL` and sets up completion in its startup file: `~/.zshrc`, `~/.bash_profile` on macOS (`~/.bashrc` elsewhere), or `~/.config/fish/completions/forge.fish`. It's safe to re-run, since it removes any earlier setup first, including `source <(forge completion zsh)` lines added by hand. Pass a shell to pick one (`forge completion install zsh`); `forge completion uninstall` removes it again. Bash also needs the `bash-completion` package (`brew install bash-completion@2` on macOS).

**Updating:** pull the latest code and run `go install ./cmd/forge` again. **Uninstalling:** run `forge completion uninstall`, then `rm ~/go/bin/forge`. To also delete your settings, remove `~/.config/forge`, delete any `forge-cli` entries in Keychain Access, and remove the agent skill if you installed it (`rm -r ~/.claude/skills/forge`).

---

## Quick start

**1. Register an API.** Give it a name and point it at its OpenAPI spec:

```
$ forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
Added petstore: Swagger Petstore - OpenAPI 3.0 1.0.27, 19 commands
Active API is now petstore. Run "forge commands" to see what you can do.
```

**2. See what it can do:**

```
$ forge commands pet --required-only
DESCRIPTION                                 COMMAND
Add a new pet to the store.                 forge pet add-pet --name <string> --photo-urls <string>,...
Finds Pets by status.                       forge pet find-pets-by-status --status <available|pending|sold>
Find pet by ID.                             forge pet get-pet-by-id --pet-id <integer>
Deletes a pet.                              forge pet delete-pet --pet-id <integer>
...
```

**3. Log in,** if the API needs credentials. The credential type and header name come from the spec:

```
$ forge auth login
Logging in for scheme "api_key" (apiKey). Others: petstore_auth; choose with --scheme.
Token:                      ← typing is hidden
Logged in to petstore: header api_key: ••••4321 for petstore3.swagger.io
Stored in OS keychain.
```

**4. Call it:**

```sh
forge pet find-pets-by-status --status available
forge pet get-pet-by-id --pet-id 10 -o yaml
forge pet add-pet --name rex --photo-urls https://example.com/rex.jpg --dry-run   # preview as curl
```

**5. Add more APIs and switch between them:**

```sh
forge add billing ./specs/billing.yaml --server https://billing.internal.example.com
forge use billing
forge apis
```

---

## How it works

forge reads the spec each time it runs and builds the commands from it:

| In the OpenAPI spec | Becomes |
|---|---|
| The operation's first `tag` (or, if untagged, the first path segment) | A **command group**: `forge pet ...` |
| `operationId` (or method + path) | A **command**: `forge pet get-pet-by-id` |
| Path, query, header and cookie parameters | **Flags**: `--pet-id 10` |
| JSON request body properties | **Flags**, with dotted names for nested fields: `--owner.name` |
| `summary` / `description` | Help text |
| `enum` | Allowed values, validated and tab-completed |
| `servers` | Default base URL |
| `securitySchemes` | What `forge auth login` sets up |

Names are converted to kebab-case: `getPetById` becomes `get-pet-by-id`, and `X-Request-ID` becomes `--x-request-id`.

---

## Managing APIs (profiles)

Every API you register is a **profile**: a spec plus its own settings, filters, login, environment variables and trusted hosts. One profile is **active** at a time, and all commands, including `forge auth login`, apply to it.

### Add an API

```sh
forge add <name> <spec-file-or-url> [flags]
forge add --name <name> --spec <spec-file-or-url> [flags]
```

```sh
forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
forge add billing ./specs/billing.yaml --server https://billing.internal.example.com --read-only
forge add stripe ./stripe.json --include-group customers --include-group charges --use
```

The spec is loaded and checked before anything is saved. Relative file paths are stored as absolute paths, so the API works from any directory. The first API you add becomes active.

| Flag | What it does |
|---|---|
| `--server <url>` | Base URL to call, overriding the spec's `servers` |
| `--use` | Make this the active API |
| `--force` | Replace an existing API with the same name. Its login is deleted if the spec, server or trusted hosts change. |
| `--include-group <g>` / `--exclude-group <g>` | Only expose, or hide, a command group (repeatable) |
| `--include-tag <t>` / `--exclude-tag <t>` | Only expose, or hide, operations with a tag (repeatable) |
| `--include-path <glob>` / `--exclude-path <glob>` | Filter by path, e.g. `/v1/**` (repeatable) |
| `--exclude-operation <id>` | Hide an operation by `operationId` (repeatable) |
| `--exclude-deprecated` | Hide deprecated operations |
| `--read-only` | Only expose GET, HEAD and OPTIONS operations |
| `--trust-host <host>` | Extra host allowed to receive credentials (see [Trusted hosts](#trusted-hosts)) |
| `--env-prefix <PREFIX>` | Environment variable prefix (default: the name, upper-cased) |
| `--no-check` | Save without loading the spec |
| `--file <apis.toml>` | Register every API in a TOML file ([Sharing](#sharing-apis-with-a-team)) |

> **Groups or tags?** Prefer `--include-group` and `--exclude-group`. Groups are the names you see in `forge commands`, and they work for every spec. Tag filters only work when the spec actually uses tags. If your filters hide everything, `forge add` warns you and lists the groups you can choose from.

**Path globs:** `*` matches within one path segment, `**` matches across segments, and `/admin/**` also matches `/admin` itself.

### Switch, list and remove

```sh
forge apis               # pick the active API with the arrow keys
forge use                # same picker
forge use billing        # switch directly by name
forge apis --list        # print the table instead (alias: -l)
forge remove billing     # unregister it and delete its stored login (alias: rm)
```

On a terminal, `forge apis` and `forge use` open an interactive picker:

```
Switch API  type to filter
  ● petstore  Swagger Petstore - OpenAPI 3.0 1.0.27  header login
❯   stripe    Stripe API                             bearer login
    billing   Billing API 1.0                        no login
↑/↓ move · enter select · esc cancel
```

| Key | Action |
|---|---|
| ↑ / ↓ (or Ctrl-P / Ctrl-N) | Move |
| Type letters | Filter by name or title (Backspace to undo) |
| Enter | Make the highlighted API active |
| Esc / Ctrl-C | Cancel, leaving the active API unchanged |

● marks the active API. When the output is piped, or with `--list`, you get a plain table instead, so scripts and agents are never stuck waiting for keyboard input:

```
$ forge apis --list
   NAME      TITLE                                  LOGIN   FILTERS                   SPEC
●  petstore  Swagger Petstore - OpenAPI 3.0 1.0.27  header  -                         https://petstore3.swagger.io/...
   stripe    Stripe API                             bearer  groups=customers,charges  /Users/me/specs/stripe.json

● = active. Switch with forge use <name>.
```

When it isn't on a terminal, `forge use` with no name prints the active API, where it came from, and its login state.

### Use another API for one command

You don't have to switch to run a single command against another API:

```sh
forge --api stripe customers get-customers --limit 3      # a registered API, one command only
FORGE_API=stripe forge commands                           # for this shell session
forge --spec ./draft-openapi.yaml routes                  # an unregistered spec, ad hoc
```

forge picks the API in this order: `--spec`, then `--api`, then `$FORGE_SPEC`, then `$FORGE_API`, then the active API.

### The registry file

APIs are stored in `~/.config/forge/apis.toml` (run `forge apis --path` to see the exact path). You can edit it by hand:

```toml
active = "petstore"

[apis.petstore]
  spec = "https://petstore3.swagger.io/api/v3/openapi.json"

[apis.billing]
  spec = "/Users/me/specs/billing.yaml"
  server = "https://billing.internal.example.com"
  trusted_hosts = ["billing-staging.internal.example.com"]
  exclude_tags = ["admin"]
  read_only = true

[apis.stripe]
  spec = "/Users/me/specs/stripe.json"
  env_prefix = "STRIPE"
  include_groups = ["customers", "charges"]
  exclude_deprecated = true
```

All keys are listed in the [configuration reference](#registry-file-apistoml). Secrets are never stored in this file.

### Sharing APIs with a team

```sh
forge apis --toml > team-apis.toml       # export your APIs (no secrets included)
forge add --file team-apis.toml          # import them on another machine
```

An import checks every spec and reports any that fail without stopping the others. Existing names are skipped unless you pass `--force`. Relative spec paths in the file are resolved against the file's own location, so you can commit `team-apis.toml` next to a `specs/` folder in a repository.

---

## Finding commands

| Command | Shows |
|---|---|
| `forge commands [group]` | Every command with its description and full invocation |
| `forge commands --required-only` | Only the arguments you must provide |
| `forge commands --markdown` | The same table as Markdown |
| `forge <group> --help` | The commands in a group |
| `forge <group> <command> --help` | Every flag, with types, allowed values and the HTTP method and path |
| `forge routes` | Each HTTP route and the command that calls it |
| `forge routes --excluded` | Routes hidden by your filters |
| `forge tools --format list` | Tool names for AI agents ([see below](#using-forge-with-ai-agents)) |

**Reading `forge commands`:**

```
forge pet find-pets-by-status --status <available|pending|sold>
forge pet add-pet --name <string> --photo-urls <string>,... [--category.name <string>] [-d <json|@file|->]
```

| Notation | Meaning |
|---|---|
| `--flag <type>` | Required |
| `[--flag <type>]` | Optional |
| `<a\|b\|c>` | One of these values |
| `<type>,...` | Comma-separated list |
| `<json>` | A JSON value |
| `<file>` | Path to a file to upload |
| `[--flag]` with no value | On/off switch |
| `-d <json\|@file\|->` | Request body: inline, from a file, or from stdin |

---

## Running commands

### Flags and values

| Spec type | Example |
|---|---|
| string | `--name rex` |
| integer, number | `--limit 10`, `--price 9.99` |
| boolean | `--express` or `--express=false` |
| array | `--tags a,b` or `--tags a --tags b` |
| enum | `--status sold` (other values are rejected; Tab completes) |
| object | `--metadata '{"color":"brown"}'` |

Required flags are checked before anything is sent. If a parameter name clashes with a global flag or another parameter, the flag gets a location prefix, such as `--header-...` or `--body-...`.

### Request bodies

JSON body properties are flags. Nested objects use dots:

```sh
forge pet add-pet --name rex --category.name dogs --photo-urls https://example.com/rex.jpg
```

For anything else, send the body with `-d` / `--body`:

```sh
forge pet add-pet -d '{"name": "rex", "photoUrls": []}'      # inline JSON
forge pet add-pet -d @pet.json                               # from a file
cat pet.json | forge pet add-pet -d -                        # from stdin
forge pet add-pet -d @pet.json --name max                    # field flags override values in the file
```

- **Forms:** form-encoded bodies are supported.
- **File uploads:** multipart file fields take a file path, e.g. `--file ./photo.jpg`.
- **Several content types:** if an operation accepts more than one, choose with `--content-type`.

### Output

```sh
forge pet get-pet-by-id --pet-id 10              # pretty JSON (default)
forge pet get-pet-by-id --pet-id 10 -o yaml      # YAML
forge pet get-pet-by-id --pet-id 10 -o raw       # body exactly as received
forge pet get-pet-by-id --pet-id 10 | jq .name   # JSON is easy to pipe
```

Responses that aren't JSON are printed as-is.

### Global flags

These work on every API command:

| Flag | What it does |
|---|---|
| `-o, --output json\|yaml\|raw` | Output format |
| `--dry-run` | Print the request as a `curl` command instead of sending it (secrets redacted) |
| `-v, --verbose` | Log the full request and response to stderr (secrets redacted) |
| `-i, --include` | Print the response status and headers to stderr |
| `-H, --header "Key: Value"` | Add a request header (repeatable) |
| `--server <url>` | Use a different base URL (credentials are only sent to [trusted hosts](#trusted-hosts)) |
| `--timeout 30s` | Request timeout (default 1m) |
| `--api <name>` | Use another registered API for this command |
| `--spec <file\|url>` | Use an unregistered spec for this command |
| `--no-color` | Disable colors |

### Colors

On a terminal, forge colors its output: highlighted JSON and YAML, HTTP methods colored by type (GET green, POST yellow, PUT/PATCH blue, DELETE red), response status colored by class, styled help pages and tables, and ✓ / ! / ✗ status markers.

**Piped or redirected output is always plain text,** with no escape codes in `forge ... | jq`, in files, or in what an AI agent reads. To turn colors off on a terminal, use `--no-color` or set `NO_COLOR=1`. To force them on, for example when piping into `less -R`, set `FORCE_COLOR=1`. `NO_COLOR` wins over `FORCE_COLOR`.

### Exit codes and errors

- **Exit code 0:** success, with the response body on stdout.
- **Exit code 1:** any error. That includes HTTP 4xx/5xx responses, whose body is printed to **stderr** so stdout stays clean for pipes.

```sh
if ! forge pet get-pet-by-id --pet-id 999 > pet.json; then echo "lookup failed"; fi
```

---

## Authentication

### Log in

```sh
forge auth login
```

This logs in to the **active API** only. forge reads the spec's security schemes to decide what to ask for:

| The spec says | `forge auth login` sets up |
|---|---|
| `apiKey` in a header | That header, e.g. `X-API-Key: <key>` |
| `apiKey` in a query parameter or cookie | That parameter or cookie |
| `http` / `bearer` | `Authorization: Bearer <token>` |
| `http` / `basic` | A username and password |
| `oauth2` / `openIdConnect` | An OAuth sign-in using the spec's URLs and scopes |

If the spec offers several schemes, forge picks one and tells you which. Choose explicitly with `--scheme <name>`.

### Other ways to log in

```sh
echo "$TOKEN" | forge auth login --token-stdin               # non-interactive (scripts, agents, CI)
forge auth login --header X-API-Key                          # any header
forge auth login --header Authorization --prefix "Token "    # custom scheme: "Authorization: Token <key>"
forge auth login --query api_key                             # query parameter
forge auth login --type basic --username alice               # prompts for the password
forge auth login --type bearer                               # plain bearer token
forge --api billing auth login                               # log in to a non-active API
```

> Secrets are entered at a hidden prompt or piped in with `--token-stdin`, `--password-stdin` or `--client-secret-stdin`. Avoid `--token <value>`, because it ends up in your shell history.

### OAuth2

```sh
# Browser sign-in (authorization code + PKCE). URLs and scopes come from the spec.
forge auth login --type oauth2 --client-id <id>

# No browser on this machine (SSH, containers): shows a URL and a code to enter elsewhere.
forge auth login --type oauth2 --flow device_code --client-id <id> \
  --device-url https://idp.example.com/oauth/device --token-url https://idp.example.com/oauth/token

# Machine-to-machine
forge auth login --type oauth2 --flow client_credentials --client-id <id> --client-secret-stdin < secret.txt
```

| Flag | Purpose |
|---|---|
| `--flow authorization_code\|device_code\|client_credentials` | OAuth flow (default: browser if the spec has an authorization URL) |
| `--client-id`, `--client-secret-stdin` | Your OAuth app's credentials |
| `--auth-url`, `--token-url`, `--device-url` | Endpoints, if the spec doesn't provide them |
| `--scope <s>` | Scopes to request (repeatable; default: what the exposed operations need) |
| `--redirect-url <url>` | The exact loopback redirect your OAuth app has registered, e.g. `http://127.0.0.1:8085/callback`. The default is a random port. |
| `--no-browser` | Print the sign-in URL instead of opening a browser |
| `--login-timeout 5m` | How long to wait for you to finish signing in |

Access tokens are refreshed automatically when they expire, and the new token is saved. If a token can't be refreshed, forge asks you to log in again.

> **Browser sign-in:** your OAuth app must allow a loopback redirect such as `http://127.0.0.1:<port>/callback`. If the provider requires an exact URL, register one and pass it with `--redirect-url`.

### Check, use and remove a login

```sh
forge auth status    # profile, trusted hosts, each scheme and where its credential comes from
forge auth token     # print the current token (refreshing it first), e.g. for curl
forge auth logout    # delete the stored login for the active API
```

```
$ forge auth status
Profile: petstore
Trusted hosts: petstore3.swagger.io

SCHEME         TYPE    CONFIGURED  SOURCE
api_key        apiKey  yes         api key in header "api_key" from $PETSTORE_API_KEY; else stored header api_key: ••••4321 ...
petstore_auth  oauth2  no          bearer token from $PETSTORE_PETSTORE_AUTH or $PETSTORE_TOKEN; else no stored login
```

### Where credentials are stored

- **The OS keychain:** macOS Keychain, Windows Credential Manager, or Secret Service on Linux. Each one is stored under the service `forge-cli`, with the account `api:<name>`.
- **A private file when there's no keychain:** `~/.config/forge/credentials.json`, readable only by you (mode 0600).
- **Forcing one:** set `FORGE_CREDENTIAL_STORE=keychain` or `FORGE_CREDENTIAL_STORE=file`.
- **Never in `apis.toml`,** so exporting or sharing your APIs never shares secrets.

### Environment variables (CI and overrides)

Credentials in environment variables take precedence over a stored login. The prefix is the API's name in upper case (or `--env-prefix`), and the scheme name comes from the spec. Run `forge auth status` to see the exact variable names.

| Scheme type | Variables |
|---|---|
| apiKey | `<PREFIX>_<SCHEME>` |
| bearer, oauth2, openIdConnect | `<PREFIX>_<SCHEME>`, then `<PREFIX>_TOKEN` |
| basic | `<PREFIX>_<SCHEME>_USERNAME` and `<PREFIX>_<SCHEME>_PASSWORD` |

```sh
PETSTORE_API_KEY=abc123 forge pet get-pet-by-id --pet-id 10
STRIPE_TOKEN=sk_test_... forge --api stripe customers get-customers
```

### Trusted hosts

Credentials, whether stored or from environment variables, are **only sent to the API's trusted hosts**:

- the profile's `server`
- the hosts in the spec's `servers`
- anything listed in `trusted_hosts` (`forge add ... --trust-host <host>`)

A request to any other host is refused before it's sent:

```
$ forge store get-inventory --server https://attacker.example.com
Error: refusing to send petstore credentials to attacker.example.com: it isn't a trusted host for this API
(trusted: petstore3.swagger.io). To allow it, add it to trusted_hosts for [apis.petstore] ...
```

This protects you from typos and from AI agents being tricked into sending your token somewhere else. A stored login also remembers the hosts it was created for. If you later change a profile's server or trusted hosts, run `forge auth login` again.

Requests that carry no credentials, such as calls to public endpoints, can go to any host.

---

## Using forge with AI agents

forge is designed so an agent can discover and call an API through the shell.

**1. Discover:** a compact index of every command:

```sh
forge commands --required-only
forge tools --format list
```

**2. Inspect one tool:** description, input JSON Schema, auth requirements and response shape:

```sh
forge tools pet_find_pets_by_status
```

**3. Call it:** either with flags, or with JSON matching the input schema:

```sh
forge pet find-pets-by-status --status available
forge call pet_find_pets_by_status '{"status": "available"}'
echo '{"name": "rex", "photo-urls": ["https://example.com/rex.jpg"]}' | forge call pet_add_pet -
```

Input property names are the flag names. Errors are written for an agent to act on: unknown or missing properties, wrong types, invalid values, and "did you mean" suggestions for misspelled tool names.

### Export tool definitions

```sh
forge tools --format anthropic > tools.json   # Claude Messages API "tools" array
forge tools --format openai > tools.json      # OpenAI Chat Completions "tools" array
forge tools                                   # full catalog: API info, how to call, auth status, tools
forge tools pet                               # just one group
```

For large APIs, response schemas are included only when 10 or fewer tools are selected, and they're limited to 2 levels deep. Adjust with `--response-schemas` and `--response-depth`.

### Agent skill

[`skills/forge`](skills/forge/SKILL.md) is an [Agent Skill](https://agentskills.io) that teaches an agent to use forge. It covers registering APIs, switching between them, checking logins (and leaving the login itself to you), and finding and running commands. Copy the folder to wherever your agent loads skills from, for example for Claude Code:

```sh
mkdir -p ~/.claude/skills && cp -r skills/forge ~/.claude/skills/    # every project
mkdir -p .claude/skills && cp -r skills/forge .claude/skills/        # one project
```

### Instructions to give your agent

If your agent doesn't support skills, paste this into its system prompt or `CLAUDE.md`:

```markdown
You can call the <API name> API with the `forge` CLI.
- Run `forge commands --required-only` to see available commands.
- Run `forge tools <tool_name>` for a command's inputs and response format.
- Run commands with flags, or `forge call <tool_name> '<json>'`.
- Use `--dry-run` to preview a request without sending it.
- Exit code 1 means failure; the error is on stderr.
- Never pass --server, and never print or ask for credentials.
```

### Keep agents safe

- **Give agents a restricted profile,** e.g. `forge add petstore-agent <spec> --read-only --exclude-group admin`, and start them with `FORGE_API=petstore-agent`.
- **Log that profile in with a token limited to what the agent needs.**
- **Rely on trusted hosts.** They stop credentials from being sent to unknown hosts, even if the agent is manipulated.

---

## Configuration reference

### Environment variables

| Variable | Purpose |
|---|---|
| `FORGE_API` | Registered API to use for this shell (overrides the active API) |
| `FORGE_SPEC` | Unregistered spec file or URL to use for this shell |
| `FORGE_CONFIG` | Path to the registry file (default `~/.config/forge/apis.toml`) |
| `XDG_CONFIG_HOME` | Base config directory (default `~/.config`) |
| `FORGE_CREDENTIAL_STORE` | `keychain` or `file`, to force where logins are stored |
| `NO_COLOR` | Disable colors ([no-color.org](https://no-color.org)) |
| `FORCE_COLOR` | Use colors even when output is piped |
| `<PREFIX>_SERVER` | Override the base URL for an API (credentials only go to [trusted hosts](#trusted-hosts)) |
| `<PREFIX>_<SCHEME>`, `<PREFIX>_TOKEN`, ... | Credentials ([details](#environment-variables-ci-and-overrides)) |

### Files

| Path | Contents |
|---|---|
| `~/.config/forge/apis.toml` | Registered APIs and the active one. Safe to share. |
| `~/.config/forge/credentials.json` | Logins, only when no OS keychain is available (mode 0600) |
| OS keychain, service `forge-cli` | Logins, one entry per API (`api:<name>`) |

### Registry file (`apis.toml`)

| Key | Type | Purpose |
|---|---|---|
| `active` | string | Active API name |
| `[apis.<name>]` | table | One registered API |
| `spec` | string | Spec file path or URL (required) |
| `server` | string | Base URL override |
| `trusted_hosts` | list | Extra hosts allowed to receive credentials |
| `env_prefix` | string | Environment variable prefix |
| `include_groups`, `exclude_groups` | list | Command group filters |
| `include_tags`, `exclude_tags` | list | Tag filters |
| `include_paths`, `exclude_paths` | list | Path glob filters |
| `exclude_operations` | list | `operationId`s to hide |
| `exclude_deprecated` | bool | Hide deprecated operations |
| `read_only` | bool | Only GET, HEAD and OPTIONS |
| `title`, `description`, `added` | | Filled in by `forge add`; informational |

---

## Troubleshooting

| Problem | Fix |
|---|---|
| `No API is active yet` | Register one with `forge add <name> <spec>`, or switch with `forge use <name>`. |
| `unknown command "x"` | Command names come from the spec. Run `forge commands` or `forge <group> --help`. |
| `missing required flag(s): --pet-id` | Add the flag. `forge commands --required-only` lists what each command needs. |
| `invalid value "x" for status (one of: ...)` | Use one of the listed values (Tab completes them). |
| `no API server URL ... relative` | The spec has no full server URL, which matters for specs loaded from a file or embedded (a spec loaded from a URL defaults to that URL's host). Set one with `forge add <name> <spec> --server https://... --force`. |
| `401 Unauthorized` / `403 Forbidden` | Run `forge auth status`, then `forge auth login`. Check you're on the right profile with `forge use`. |
| `refusing to send ... credentials to <host>` | The host isn't trusted for this API. If it's legitimate, add it to `trusted_hosts` and log in again. |
| `the stored login is bound to <host>` | The profile's server changed since you logged in. Run `forge auth login`. |
| `the filters hide every operation` | The spec probably has no tags. Use `--include-group` with a group listed in the warning. |
| `would share environment variables` | Two API names map to the same env prefix. Pick another name or set `--env-prefix`. |
| `loading <api> ...: unexpected status` / offline | A spec registered by URL is downloaded on every run. Download it once (`curl -o spec.json <url>`) and run `forge add <name> ./spec.json --force`. |
| Slow startup on a huge spec | Same fix: use a local copy, and filter to the groups you need. |
| `command not found: compdef`, or Tab doesn't complete | Run `forge completion install`, then open a new terminal. It replaces any older setup and turns on zsh's completion system if needed. |
| Something else is wrong | Run with `--dry-run` to see the exact request, or `-v` to see the request and response. |

---

## Building your own CLI

`forge` is built on a Go framework you can use directly to ship a dedicated CLI for your API. The spec is embedded in the binary, so your users don't need to configure anything. See [`examples/petstore`](examples/petstore) for a complete example:

```sh
go run ./examples/petstore --help
go run ./examples/petstore pet list --status available --dry-run
```

### Minimal CLI

```go
package main

import (
	_ "embed"

	"forge-cli/auth"
	"forge-cli/filter"
	"forge-cli/forge"
	"forge-cli/transform"
)

//go:embed openapi.yaml
var spec []byte

func main() {
	forge.Main(
		forge.WithName("myapi"), // binary name; env prefix MYAPI_
		forge.WithVersion("1.0.0"),
		forge.WithSpecData(spec),
		forge.WithCredentialStore(auth.DefaultStore(), "myapi"), // enables "myapi auth login"
		forge.WithFilters(filter.ExcludeTags("internal"), filter.ExcludeDeprecated()),
		forge.WithTransforms(transform.TrimGroupFromName(), transform.PositionalPathParams()),
	)
}
```

Your CLI gets the same built-in commands as `forge`: `commands`, `routes`, `auth`, `tools`, `call` and `completion`. That includes `myapi completion install`, which sets up completion in the user's shell. If you assemble your own root command instead of using `forge.Main` or `App.Command`, call `forge.AddCompletionCommand(root)` after adding its subcommands to get the same `completion` command.

### Pipeline and packages

```
load spec ─► normalize ─► name ─► filter ─► transform ─► dedupe names ─► cobra commands
```

| Package | Responsibility | Extend with |
|---|---|---|
| `spec` | Load a file, URL or bytes; convert Swagger 2.0; build the operation model | `WithAPI` to bring your own loader |
| `naming` | Group, command and flag names | `naming.Namer`, `WithNamer` |
| `filter` | Which operations are exposed | `filter.Matcher`, `filter.Filter` |
| `transform` | Renames, regrouping, hidden or preset parameters, descriptions | `transform.Transform` |
| `auth` | Credentials, OAuth flows, keychain storage, host checks | `auth.Provider`, `auth.Store` |
| `transport` | HTTP middleware: retries, user agent, debug logging | `transport.Middleware` |
| `request` | Building requests and rendering curl | — |
| `output` | Response formatting | `output.Formatter`, `WithFormatter` |
| `style` | Terminal colors, tables, highlighting, spinner (plain when not a terminal) | `style.For(w)`, `style.NewTable` |
| `catalog` | Agent tool definitions and command synopses | `catalog.Build`, `App.Tools` |
| `forge` | Wiring, hooks and built-in commands | `WithRequestHook`, `WithResponseHook`, `WithCommandHook`, `WithCommands` |

### Filters

An operation is exposed only if **every** filter keeps it. Check the result with `routes` and `routes --excluded`.

```go
forge.WithFilters(
	filter.ExcludeTags("admin", "internal"),
	filter.IncludePaths("/v1/**"),
	filter.Exclude(filter.Route("DELETE /**", "* /internal/*")),
	filter.ExcludeOperations("dangerousReset"),
	filter.ExcludeDeprecated(),
	filter.ExcludeExtension("x-internal"),
	filter.ReadOnly(),
	filter.Exclude(filter.All(filter.Tag("users"), filter.Method("DELETE"))),
	filter.Filter(func(op *spec.Operation) bool { return len(op.Path) < 50 }),
)
```

### Transforms

Transforms apply to every exposed operation. Scope them with `transform.When(matcher, ...)`.

```go
forge.WithTransforms(
	transform.TrimGroupFromName(),                         // pet get-pet-by-id -> pet get-by-id
	transform.RenameOperation("findPetsByStatus", "list"),
	transform.RenameGroup("pet-photos", "photos"),
	transform.GroupByPathSegment(0),                       // group by path instead of tag
	transform.Flatten(),                                   // no groups
	transform.When(filter.Tag("pet"), transform.PositionalPathParams()), // pet get 42
	transform.When(filter.OperationID("listPets"), transform.Aliases("ls"), transform.Example("...")),
	transform.When(filter.Path("/beta/**"), transform.Hide()),
	transform.Preset("api-version", "2024-01-01"),         // sent unless the user overrides it
	transform.ParamFromEnv("orgId", "MYAPI_ORG"),          // environment variable fallback
	transform.HideParam("X-Trace-Id"),
	transform.RemoveParam("internalFlag"),
	transform.RenameFlag("X-Request-ID", "request-id"),
	transform.ShortFlag("limit", "l"),
)
```

### Better descriptions

Help text and agent tool definitions use the spec's descriptions. Improve them without editing the spec:

```go
transform.When(filter.OperationID("listPets"), transform.Summary("..."), transform.Description("..."))
transform.DescribeParam("status", "Only return pets in these states")
docs, _ := transform.DocsFromYAML(docsYAML) // bulk overlay; see examples/petstore/docs.yaml
```

### Spec extensions

API authors can shape the CLI from the spec itself. Turn this off with `WithoutVendorExtensions`.

| Extension | On | Effect |
|---|---|---|
| `x-cli-ignore: true` | operation, parameter | Not exposed |
| `x-cli-hidden: true` | operation, parameter | Hidden from help |
| `x-cli-name` | operation, parameter | Command name or flag name |
| `x-cli-group` | operation | Group name |
| `x-cli-aliases: [ls]` | operation | Aliases |
| `x-cli-env` | parameter | Environment variable fallback |
| `x-cli-short` | parameter | One-letter flag shorthand |
| `x-cli-summary`, `x-cli-description` | operation (description also on parameters) | Override documentation |

### Auth, hooks and HTTP

```go
forge.WithCredentialStore(auth.DefaultStore(), "myapi")                // auth login/logout/status/token
forge.WithTrustedHosts("staging.example.com")                          // extra hosts allowed to get credentials
forge.WithAuth("oauth", auth.Bearer{Token: auth.SecretFunc("vault", readFromVault)})
forge.WithFallbackAuth(auth.APIKey{Name: "X-Key", In: spec.InHeader, Value: auth.Env("MY_KEY")})
forge.WithMiddleware(transport.UserAgent("myapi/1.0"), transport.Retry(3, 500*time.Millisecond))
forge.WithRequestHook(func(ctx context.Context, op *spec.Operation, req *http.Request) error { ... })
forge.WithResponseHook(func(ctx context.Context, op *spec.Operation, resp *output.Response) error { ... })
forge.WithFormatter("table", myTableFormatter)                         // -o table
forge.WithCommands(myCustomCmd)                                        // hand-written commands
```

`App.Tools(catalog.Options{})` returns the agent tool definitions in Go, for example to serve them over MCP.

---

## Development

```sh
go test ./...                    # all tests
go vet ./...
go run ./cmd/forge --help        # run without installing
go run ./examples/petstore --help
```

```
cmd/forge/          the forge binary: API registry, profiles, add/use/apis/remove
examples/petstore/  a dedicated CLI with an embedded spec, filters, transforms and docs overlay
forge/              framework entry point: options, command building, built-in commands, shell completion
skills/forge/       agent skill that teaches AI agents to use forge (SKILL.md plus references)
spec/ naming/ filter/ transform/ catalog/ request/ output/ transport/ auth/ style/
```

Tests never touch your real keychain or config. They use temporary directories and the file credential store.

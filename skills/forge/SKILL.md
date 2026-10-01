---
name: forge
description: Call HTTP APIs from the shell with the forge CLI, which turns OpenAPI specs into commands. Covers registering an API from a spec file or URL, switching between registered APIs, checking and setting up authentication, and finding and running the right command. Use when a task involves forge, calling a REST API that is (or should be) registered in forge, or adding an OpenAPI spec as a CLI.
compatibility: Requires the forge binary on PATH (go install github.com/jleva12/forge-cli/cmd/forge@latest).
---

# forge

forge turns an OpenAPI spec into a CLI. Each registered API is a named profile with its own spec, settings and login, and one profile is active at a time. Every endpoint becomes a command: `forge <group> <command> --flag value`.

Command names, flags and allowed values come from each API's spec, so don't guess them. Look them up with the commands in step 5. Output is plain text when piped, so you can read it directly.

## 1. See which APIs are registered

```sh
forge apis    # registered APIs; ● marks the active one
forge use     # the active API, where that choice came from, and its login state
```

If the API you need isn't listed, register it (step 2).

## 2. Register an API

```sh
forge add <name> <spec-file-or-url>
```

- `<name>` is short, lowercase and unique, e.g. `billing`. It's what you pass to `--api`, and it prefixes the API's environment variables (`BILLING_TOKEN`).
- The spec is loaded and checked before anything is saved. Relative file paths are stored as absolute paths.
- The first API added becomes active. Later ones don't, unless you pass `--use`.
- Common flags: `--server <url>` when a spec loaded from a local file has no full server URL, `--read-only` to expose only GET, HEAD and OPTIONS, and `--include-group <g>` / `--exclude-group <g>` to limit the commands.

For every flag, filters, replacing an API and importing several at once, read [references/managing-apis.md](references/managing-apis.md) or run `forge add --help`.

## 3. Pick the API for each command

Target an API for one command with `--api <name>` instead of switching:

```sh
forge --api billing commands
forge --api billing invoices list-invoices --limit 5
```

`forge use <name>` changes the active API for the user and every shell until they switch again. Run it only when the user asks to switch.

forge picks the API in this order: `--spec <file|url>`, `--api <name>`, `$FORGE_SPEC`, `$FORGE_API`, then the active API.

## 4. Check authentication

```sh
forge --api <name> auth status
```

This lists the API's security schemes, whether each has a credential, and where it comes from, including the exact environment variable names. If a command needs a scheme that isn't configured, or a call fails with 401 or 403:

- Ask the user to run `forge --api <name> auth login` in their own terminal. It works out the credential type from the spec, reads the secret at a hidden prompt, and stores it in the OS keychain. You can't answer that prompt, and you shouldn't see the secret.
- Never ask the user to paste a token, password or API key into the chat. Never put one on a command line (`--token`, `-H "Authorization: ..."`), and never run `forge auth token`, which prints the secret.
- Credentials can also come from environment variables such as `BILLING_TOKEN`. forge uses them automatically when they're set.

Login types (API key, bearer, basic, OAuth2), trusted hosts and auth errors are covered in [references/authentication.md](references/authentication.md).

## 5. Find the right command

Go from broad to specific. Add `--api <name>` to any of these:

| Command | Shows |
|---|---|
| `forge commands` | Every command with a description and its full invocation |
| `forge commands <group> --required-only` | One group, with only the arguments you must provide |
| `forge routes` | Each HTTP method and path, and the command that calls it |
| `forge <group> --help` | The commands in a group |
| `forge <group> <command> --help` | Every flag with its type, allowed values and defaults, plus the HTTP method and path |
| `forge tools --format list` | Tool names, for `forge call` |
| `forge tools <tool_name>` | The input JSON Schema, required auth and response schema |

How to read invocations: `[...]` is optional, `<a|b|c>` means one of those values, `<type>,...` is a comma-separated list, `<json>` is a JSON value, and `-d <json|@file|->` is the request body.

## 6. Run it

With flags (examples use the petstore API):

```sh
forge --api petstore pet find-pets-by-status --status available
forge --api petstore pet add-pet --name rex --photo-urls https://example.com/rex.jpg
```

Or with JSON whose property names are the flag names. A tool's name is its command path with underscores:

```sh
forge --api petstore call pet_find_pets_by_status '{"status": "available"}'
echo '{"name": "rex", "photo-urls": ["https://example.com/rex.jpg"]}' | forge --api petstore call pet_add_pet -
```

- **Request bodies:** body fields are flags, with dots for nesting (`--category.name dogs`). Or send the whole body with `-d '<json>'`, `-d @file.json`, or `-d -` for stdin.
- **Preview:** `--dry-run` prints the request as a curl command without sending it.
- **Output:** JSON on stdout. Use `-o yaml` or `-o raw` for other formats, and pipe to `jq` to pick out fields.
- **Errors:** exit code 0 means success. Exit code 1 means failure, with the error or the API's error response on stderr. Messages say what's wrong (missing flags, invalid values, "did you mean" for misspelled names), so fix the command instead of retrying it unchanged.
- **Debugging:** `-v` logs the full request and response to stderr, with secrets redacted.

## Rules

- Commands that use POST, PUT, PATCH or DELETE change real data. Run them only when the user asked for that change. If in doubt, show the `--dry-run` output and ask first.
- Ask before running `forge use`, `forge remove`, `forge add --force` or `forge auth logout`. They change the user's saved setup, and the last three can delete a stored login.
- Don't pass `--server` unless the user asks. forge refuses to send credentials to hosts that aren't trusted for the API, and you shouldn't work around that.

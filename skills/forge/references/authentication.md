# Authentication

Logins belong to one API (profile). Add `--api <name>` to target an API that isn't active.

## Check

```sh
forge --api <name> auth status
```

It shows the profile, its trusted hosts, and for each security scheme: its type, whether a credential is configured, and where the credential comes from, including the environment variable names to use. `forge use` also shows the active API's login state.

## Logging in is the user's job

`forge auth login` reads secrets at a hidden prompt or opens a browser, so the user has to run it in their own terminal. Tell them the exact command to run, then check `auth status` again once they're done. Never ask for a secret in the chat, never pass one as a flag value, and never run `forge auth token` (it prints the token).

forge works out the credential type from the spec:

| The spec says | `forge auth login` sets up |
|---|---|
| `apiKey` in a header | That header, e.g. `X-API-Key: <key>` |
| `apiKey` in a query parameter or cookie | That parameter or cookie |
| `http` / `bearer` | `Authorization: Bearer <token>` |
| `http` / `basic` | A username and password |
| `oauth2` / `openIdConnect` | An OAuth sign-in using the spec's URLs and scopes |

Commands to suggest to the user:

```sh
forge --api <name> auth login                                   # type inferred from the spec
forge --api <name> auth login --scheme <scheme>                 # pick one of several schemes
forge --api <name> auth login --header X-API-Key                # API key in a custom header
forge --api <name> auth login --header Authorization --prefix "Token "
forge --api <name> auth login --type basic --username <user>
forge --api <name> auth login --type oauth2 --client-id <id>    # browser sign-in (PKCE)
forge --api <name> auth login --type oauth2 --flow device_code --client-id <id>   # no browser on this machine
forge --api <name> auth login --type oauth2 --flow client_credentials --client-id <id> --client-secret-stdin < secret.txt
```

OAuth flags: `--flow authorization_code|device_code|client_credentials`, `--auth-url`, `--token-url`, `--device-url` (when the spec doesn't provide them), `--scope` (repeatable), `--redirect-url` (the exact loopback URL the OAuth app has registered), `--no-browser`. Access tokens refresh automatically.

If the user says a secret is already in an environment variable, you can store it without seeing it:

```sh
printenv MY_TOKEN | forge --api <name> auth login --token-stdin
```

## Environment variables

Credentials in environment variables take precedence over a stored login. `auth status` shows the exact names. The prefix is the API name in upper case (or its `--env-prefix`), and the scheme name comes from the spec.

| Scheme type | Variables |
|---|---|
| apiKey | `<PREFIX>_<SCHEME>` |
| bearer, oauth2, openIdConnect | `<PREFIX>_<SCHEME>`, then `<PREFIX>_TOKEN` |
| basic | `<PREFIX>_<SCHEME>_USERNAME` and `<PREFIX>_<SCHEME>_PASSWORD` |

## Trusted hosts

forge only sends credentials to the API's trusted hosts: the profile's `server`, the hosts in the spec's `servers`, and any `--trust-host` given to `forge add`. Requests to other hosts that would carry credentials are refused before they're sent. Treat a refusal as a stop sign: tell the user rather than working around it.

## Log out

```sh
forge --api <name> auth logout    # deletes the stored login; ask the user first
```

## Problems

| Symptom | Fix |
|---|---|
| `401 Unauthorized` / `403 Forbidden` | Run `forge --api <name> auth status`. Make sure you're targeting the right API. If the needed scheme isn't configured, or the token has expired or lacks access, ask the user to run `forge --api <name> auth login`. |
| `refusing to send ... credentials to <host>` | The host isn't trusted for this API. Tell the user. If it's legitimate, they can add it with `forge add <name> <spec> --trust-host <host> --force` and log in again. |
| `the stored login is bound to <host>` | The profile's server changed since the login. Ask the user to run `forge --api <name> auth login` again. |
| `Login: none` in `forge use` | No stored login. An environment variable may still provide credentials; check `auth status`. |

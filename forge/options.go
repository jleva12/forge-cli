package forge

import (
	"context"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/filter"
	"github.com/jleva12/forge-cli/naming"
	"github.com/jleva12/forge-cli/output"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/transform"
	"github.com/jleva12/forge-cli/transport"
)

// Option configures an App.
type Option func(*App)

// RequestHook runs after the request is built and authenticated, just before
// it is sent (or printed with --dry-run). Use it to add signing, tracing, etc.
type RequestHook func(ctx context.Context, op *spec.Operation, req *http.Request) error

// ResponseHook runs after the response is read and before it is printed.
// It may modify resp (e.g. unwrap an envelope) or return an error.
type ResponseHook func(ctx context.Context, op *spec.Operation, resp *output.Response) error

// CommandHook customizes each generated command, e.g. to add examples,
// extra flags or completion functions.
type CommandHook func(op *spec.Operation, cmd *cobra.Command)

// --- Identity ---------------------------------------------------------------

// WithName sets the binary name shown in help. It also becomes the default
// environment variable prefix.
func WithName(name string) Option { return func(a *App) { a.name = name } }

// WithDescription sets the root command's short and long descriptions.
func WithDescription(short, long string) Option {
	return func(a *App) { a.short, a.long = short, long }
}

// WithVersion enables --version.
func WithVersion(v string) Option { return func(a *App) { a.version = v } }

// WithEnvPrefix sets the prefix for environment variables such as
// <PREFIX>_SERVER and <PREFIX>_TOKEN. Defaults to the upper-cased name.
func WithEnvPrefix(prefix string) Option { return func(a *App) { a.envPrefix = prefix } }

// --- Spec source ------------------------------------------------------------

// WithSpec loads the spec from a file path or http(s) URL.
func WithSpec(location string) Option { return func(a *App) { a.specLocation = location } }

// WithSpecCache keeps a spec loaded from a URL on disk between runs, so it
// isn't downloaded every time the CLI starts. See spec.Cache.
func WithSpecCache(c *spec.Cache) Option { return func(a *App) { a.loadOpts.Cache = c } }

// WithSpecData uses spec bytes, typically from go:embed.
func WithSpecData(data []byte) Option { return func(a *App) { a.specData = data } }

// WithAPI uses an already loaded API (useful for tests or custom loaders).
func WithAPI(api *spec.API) Option { return func(a *App) { a.api = api } }

// WithValidation enables strict OpenAPI validation when loading.
func WithValidation() Option { return func(a *App) { a.loadOpts.Validate = true } }

// WithServerVariables fills in variables in the spec's server URLs, such as
// {tenant} in https://{tenant}.example.com, instead of their defaults.
func WithServerVariables(vars map[string]string) Option {
	return func(a *App) { a.loadOpts.Normalize.ServerVariables = vars }
}

// WithBodyFlattenDepth sets how many levels of nested body objects become
// dotted flags (default 2). Negative exposes top-level properties only.
func WithBodyFlattenDepth(depth int) Option {
	return func(a *App) { a.loadOpts.Normalize.BodyFlattenDepth = depth }
}

// --- Shaping the command tree -----------------------------------------------

// WithFilters restricts which operations are exposed. An operation must pass
// every filter.
func WithFilters(fs ...filter.Filter) Option {
	return func(a *App) { a.filters = append(a.filters, fs...) }
}

// WithTransforms adjusts names, groups and parameters after filtering.
func WithTransforms(ts ...transform.Transform) Option {
	return func(a *App) { a.transforms = append(a.transforms, ts...) }
}

// WithNamer replaces the default naming strategy.
func WithNamer(n naming.Namer) Option { return func(a *App) { a.namer = n } }

// WithoutVendorExtensions ignores x-cli-* extensions in the spec.
func WithoutVendorExtensions() Option { return func(a *App) { a.vendorExtensions = false } }

// WithGroupDescription sets the help text of a command group.
func WithGroupDescription(group, description string) Option {
	return func(a *App) { a.groupDescriptions[group] = description }
}

// WithCommandHook customizes every generated operation command.
func WithCommandHook(h CommandHook) Option {
	return func(a *App) { a.commandHooks = append(a.commandHooks, h) }
}

// WithCommands adds hand-written commands to the root (e.g. "login").
func WithCommands(cmds ...*cobra.Command) Option {
	return func(a *App) { a.extraCommands = append(a.extraCommands, cmds...) }
}

// WithoutBuiltins removes the built-in "routes", "auth", "tools" and "call" commands.
func WithoutBuiltins() Option { return func(a *App) { a.builtins = false } }

// --- HTTP -------------------------------------------------------------------

// WithBaseURL sets the default server URL, overriding the spec's servers.
func WithBaseURL(u string) Option { return func(a *App) { a.baseURL = u } }

// WithHTTPClient sets the base HTTP client. Middleware wraps its transport.
func WithHTTPClient(c *http.Client) Option { return func(a *App) { a.httpClient = c } }

// WithMiddleware adds HTTP middleware (retries, headers, tracing, ...).
func WithMiddleware(mws ...transport.Middleware) Option {
	return func(a *App) { a.middleware = append(a.middleware, mws...) }
}

// WithRequestHook runs h before each request is sent.
func WithRequestHook(h RequestHook) Option {
	return func(a *App) { a.requestHooks = append(a.requestHooks, h) }
}

// WithResponseHook runs h after each response is received.
func WithResponseHook(h ResponseHook) Option {
	return func(a *App) { a.responseHooks = append(a.responseHooks, h) }
}

// --- Auth -------------------------------------------------------------------

// WithAuth sets the provider for a security scheme declared in the spec,
// replacing the environment-variable default.
func WithAuth(scheme string, p auth.Provider) Option {
	return func(a *App) { a.authOverrides[scheme] = p }
}

// WithFallbackAuth applies p to operations whose declared security can't be
// satisfied (or that declare none), e.g. for specs with missing securitySchemes.
func WithFallbackAuth(p auth.Provider) Option { return func(a *App) { a.fallbackAuth = p } }

// WithAuthCommands adds subcommands under "auth", e.g. "login" and "logout".
func WithAuthCommands(cmds ...*cobra.Command) Option {
	return func(a *App) { a.authCommands = append(a.authCommands, cmds...) }
}

// WithoutSpecAuth disables the environment-variable providers derived from
// the spec's security schemes.
func WithoutSpecAuth() Option { return func(a *App) { a.specAuth = false } }

// --- Output -----------------------------------------------------------------

// WithFormatter registers an output format selectable with -o.
func WithFormatter(name string, f output.Formatter) Option {
	return func(a *App) { a.formatters[name] = f }
}

// WithDefaultOutput sets the default -o format (default "json").
func WithDefaultOutput(name string) Option { return func(a *App) { a.defaultOutput = name } }

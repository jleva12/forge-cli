// Package auth applies credentials to outgoing requests.
//
// Providers are registered per OpenAPI security scheme name. For each request
// the Registry picks the first security requirement of the operation whose
// schemes all have usable credentials. New mechanisms (OAuth login, cloud
// signing, ...) only need to implement Provider.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jleva12/forge-cli/naming"
	"github.com/jleva12/forge-cli/spec"
)

// ErrNoCredentials means a provider has nothing to apply. The registry then
// tries the next security alternative instead of failing.
var ErrNoCredentials = errors.New("no credentials configured")

// Provider adds credentials to a request.
type Provider interface {
	Apply(ctx context.Context, req *http.Request) error
}

// Describer is optionally implemented by providers to explain in
// "auth status" where credentials come from.
type Describer interface {
	Describe() string
}

// ProviderFunc adapts a function to Provider.
type ProviderFunc func(ctx context.Context, req *http.Request) error

func (f ProviderFunc) Apply(ctx context.Context, req *http.Request) error { return f(ctx, req) }

// --- Secrets ----------------------------------------------------------------

// Secret lazily resolves a credential value.
type Secret interface {
	Value(ctx context.Context) (string, error)
	String() string // where the secret comes from, never the value
}

type staticSecret string

func (s staticSecret) Value(context.Context) (string, error) {
	if s == "" {
		return "", ErrNoCredentials
	}
	return string(s), nil
}
func (staticSecret) String() string { return "static value" }

// Static returns a fixed secret.
func Static(v string) Secret { return staticSecret(v) }

type envSecret []string

func (e envSecret) Value(context.Context) (string, error) {
	for _, name := range e {
		if v := os.Getenv(name); v != "" {
			return v, nil
		}
	}
	return "", ErrNoCredentials
}
func (e envSecret) String() string { return "$" + strings.Join(e, " or $") }

// Env reads the first non-empty environment variable.
func Env(names ...string) Secret { return envSecret(names) }

type funcSecret struct {
	desc string
	fn   func(ctx context.Context) (string, error)
}

func (f funcSecret) Value(ctx context.Context) (string, error) { return f.fn(ctx) }
func (f funcSecret) String() string                            { return f.desc }

// SecretFunc builds a secret from a function, e.g. reading a keychain or
// config file. Return ErrNoCredentials when nothing is available.
func SecretFunc(description string, fn func(ctx context.Context) (string, error)) Secret {
	return funcSecret{description, fn}
}

// FirstOf tries secrets in order.
func FirstOf(secrets ...Secret) Secret {
	descs := make([]string, len(secrets))
	for i, s := range secrets {
		descs[i] = s.String()
	}
	return SecretFunc(strings.Join(descs, ", then "), func(ctx context.Context) (string, error) {
		for _, s := range secrets {
			v, err := s.Value(ctx)
			if err == nil {
				return v, nil
			}
			if !errors.Is(err, ErrNoCredentials) {
				return "", err
			}
		}
		return "", ErrNoCredentials
	})
}

// --- Providers --------------------------------------------------------------

// APIKey sends a key in a header, query parameter or cookie.
type APIKey struct {
	Name  string
	In    spec.Location
	Value Secret
}

func (a APIKey) Apply(ctx context.Context, req *http.Request) error {
	v, err := a.Value.Value(ctx)
	if err != nil {
		return err
	}
	switch a.In {
	case spec.InQuery:
		q := req.URL.Query()
		q.Set(a.Name, v)
		req.URL.RawQuery = q.Encode()
	case spec.InCookie:
		req.AddCookie(&http.Cookie{Name: a.Name, Value: v})
	default:
		req.Header.Set(a.Name, v)
	}
	return nil
}

func (a APIKey) Describe() string {
	return fmt.Sprintf("api key in %s %q from %s", a.In, a.Name, a.Value)
}

// Bearer sends "Authorization: Bearer <token>".
type Bearer struct{ Token Secret }

func (b Bearer) Apply(ctx context.Context, req *http.Request) error {
	v, err := b.Token.Value(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+v)
	return nil
}

func (b Bearer) Describe() string { return "bearer token from " + b.Token.String() }

// Basic sends HTTP basic credentials.
type Basic struct{ Username, Password Secret }

func (b Basic) Apply(ctx context.Context, req *http.Request) error {
	u, err := b.Username.Value(ctx)
	if err != nil {
		return err
	}
	p, err := b.Password.Value(ctx)
	if err != nil && !errors.Is(err, ErrNoCredentials) {
		return err
	}
	req.SetBasicAuth(u, p)
	return nil
}

func (b Basic) Describe() string {
	return fmt.Sprintf("basic auth from %s / %s", b.Username, b.Password)
}

// FromSpec builds default providers for every security scheme, reading
// credentials from environment variables named <PREFIX>_<SCHEME>:
//
//	apiKey          -> $PREFIX_<SCHEME>
//	http bearer     -> $PREFIX_<SCHEME>, then $PREFIX_TOKEN
//	http basic      -> $PREFIX_<SCHEME>_USERNAME / _PASSWORD
//	oauth2 / oidc   -> $PREFIX_<SCHEME>, then $PREFIX_TOKEN (sent as bearer)
func FromSpec(api *spec.API, envPrefix string) map[string]Provider {
	out := map[string]Provider{}
	for name, s := range api.SecuritySchemes {
		env := naming.EnvName(envPrefix, name)
		switch {
		case s.Type == "apiKey":
			out[name] = APIKey{Name: s.ParamName, In: s.In, Value: Env(env)}
		case s.Type == "http" && s.Scheme == "basic":
			out[name] = Basic{Username: Env(env + "_USERNAME"), Password: Env(env + "_PASSWORD")}
		case s.Type == "http" || s.Type == "oauth2" || s.Type == "openIdConnect":
			out[name] = Bearer{Token: Env(env, naming.EnvName(envPrefix, "token"))}
		}
	}
	return out
}

// --- Registry -----------------------------------------------------------------

// Registry maps security scheme names to providers.
type Registry struct {
	providers map[string]Provider
	// Fallback is applied when an operation declares no security or none of
	// its requirements can be satisfied.
	Fallback Provider
}

func NewRegistry() *Registry { return &Registry{providers: map[string]Provider{}} }

func (r *Registry) Set(scheme string, p Provider) { r.providers[scheme] = p }
func (r *Registry) Get(scheme string) Provider    { return r.providers[scheme] }

// Apply authenticates req for an operation's security requirements.
// Missing credentials are not an error: the request is sent as-is and the
// server decides.
func (r *Registry) Apply(ctx context.Context, req *http.Request, reqs []spec.SecurityRequirement) error {
	if reqs != nil && len(reqs) == 0 {
		return nil // "security: []" explicitly disables auth for this operation
	}
	for _, requirement := range reqs {
		if len(requirement) == 0 {
			return nil // anonymous access is an explicit alternative
		}
		ok, err := r.tryRequirement(ctx, req, requirement)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	if r.Fallback != nil {
		if err := r.Fallback.Apply(ctx, req); err != nil && !errors.Is(err, ErrNoCredentials) {
			return err
		}
	}
	return nil
}

func (r *Registry) tryRequirement(ctx context.Context, req *http.Request, requirement spec.SecurityRequirement) (bool, error) {
	// Apply to a clone first so a half-satisfied requirement leaves no trace.
	trial := req.Clone(ctx)
	for scheme := range requirement {
		p := r.providers[scheme]
		if p == nil {
			return false, nil
		}
		if err := p.Apply(ctx, trial); err != nil {
			if errors.Is(err, ErrNoCredentials) {
				return false, nil
			}
			return false, fmt.Errorf("auth %s: %w", scheme, err)
		}
	}
	req.Header = trial.Header
	req.URL = trial.URL
	return true, nil
}

// Configured reports whether a provider currently has credentials for
// requests to target, a URL. The target matters because stored logins are
// bound to the hosts they were created for.
func Configured(ctx context.Context, p Provider, target string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	return p.Apply(ctx, req) == nil
}

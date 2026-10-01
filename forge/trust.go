package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/spec"
)

// WithTrustedHosts adds hosts (e.g. "api.example.com" or "localhost:8080")
// that may receive credentials, in addition to the configured base URL and
// the spec's servers.
func WithTrustedHosts(hosts ...string) Option {
	return func(a *App) { a.extraTrustedHosts = append(a.extraTrustedHosts, hosts...) }
}

// WithProfile names the configuration this CLI is running as (e.g. the
// registered API name). It's shown in auth messages so it's always clear
// which profile a login belongs to.
func WithProfile(name string) Option { return func(a *App) { a.profile = name } }

// WithUntrustedHostHint sets the advice shown when a request to an untrusted
// host is refused, e.g. how to add a trusted host in your CLI.
func WithUntrustedHostHint(hint string) Option { return func(a *App) { a.untrustedHint = hint } }

// TrustedHosts returns the hosts allowed to receive credentials: the base URL
// set with WithBaseURL, the spec's absolute servers and WithTrustedHosts.
// Runtime overrides (--server, $PREFIX_SERVER) are deliberately not trusted,
// so a stray or injected --server can't leak credentials.
func (a *App) TrustedHosts() []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		h := auth.NormalizeHost(raw)
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	add(a.baseURL)
	if a.api != nil {
		for _, s := range a.api.Servers {
			add(s.URL)
		}
	}
	for _, h := range a.extraTrustedHosts {
		add(h)
	}
	sort.Strings(out)
	return out
}

func (a *App) trusts(u *url.URL) bool {
	return auth.HostAllowed(u, a.TrustedHosts())
}

// applyAuth adds credentials to req, refusing when they'd go to a host
// outside the trusted set.
func (a *App) applyAuth(ctx context.Context, req *http.Request, op *spec.Operation) error {
	if a.trusts(req.URL) {
		return a.auth.Apply(ctx, req, op.Security)
	}
	// Untrusted host: proceed only if no credential would be attached.
	probe := req.Clone(ctx)
	err := a.auth.Apply(ctx, probe, op.Security)
	if err == nil && reflect.DeepEqual(probe.Header, req.Header) && probe.URL.String() == req.URL.String() {
		return nil
	}
	trusted := strings.Join(a.TrustedHosts(), ", ")
	if trusted == "" {
		trusted = "none configured"
	}
	msg := fmt.Sprintf("refusing to send %s credentials to %s: it isn't a trusted host for this API (trusted: %s)",
		a.profileLabel(), req.URL.Host, trusted)
	if a.untrustedHint != "" {
		msg += ". " + a.untrustedHint
	}
	return fmt.Errorf("%s", msg)
}

func (a *App) profileLabel() string {
	if a.profile != "" {
		return a.profile
	}
	return a.name
}

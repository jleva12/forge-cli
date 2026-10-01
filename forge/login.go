package forge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/term"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
)

// WithCredentialStore enables "auth login", "auth logout" and "auth token",
// saving credentials in store under key (e.g. the CLI or API name). Stored
// credentials are used after environment variables.
func WithCredentialStore(store auth.Store, key string) Option {
	return func(a *App) { a.credStore, a.credKey = store, key }
}

// schemeCredential applies the stored credential only if it was saved for
// scheme. With scheme "", it applies credentials saved without one, which
// happens when the spec declares no security schemes.
type schemeCredential struct {
	stored *auth.StoredProvider
	scheme string
}

func (s schemeCredential) Apply(ctx context.Context, req *http.Request) error {
	c, err := s.stored.Store.Get(s.stored.Key)
	if err != nil {
		return auth.ErrNoCredentials
	}
	if c.Scheme != s.scheme {
		return auth.ErrNoCredentials
	}
	return s.stored.Apply(ctx, req)
}

func (s schemeCredential) Describe() string {
	c, err := s.stored.Store.Get(s.stored.Key)
	if err != nil || c.Scheme != s.scheme {
		return "no stored login"
	}
	return "stored " + c.Summary()
}

// installStoredAuth layers the stored credential behind each scheme's
// environment-variable provider, and behind the fallback.
func (a *App) installStoredAuth() {
	if a.credStore == nil {
		return
	}
	stored := &auth.StoredProvider{Store: cachedStore(a.credStore), Key: a.credKey, HTTPClient: a.httpClient}
	for _, name := range sortedKeys(a.api.SecuritySchemes) {
		sc := schemeCredential{stored, name}
		if p := a.auth.Get(name); p != nil {
			a.auth.Set(name, auth.Chain(p, sc))
		} else {
			a.auth.Set(name, sc)
		}
	}
	unbound := schemeCredential{stored, ""}
	if a.auth.Fallback != nil {
		a.auth.Fallback = auth.Chain(a.auth.Fallback, unbound)
	} else {
		a.auth.Fallback = unbound
	}
}

func (a *App) loginCommands() []*cobra.Command {
	if a.credStore == nil {
		return nil
	}
	return []*cobra.Command{a.loginCommand(), a.logoutCommand(), a.tokenCommand()}
}

type loginFlags struct {
	scheme, typ, header, prefix, query                      string
	token, username                                         string
	tokenStdin, passwordStdin, clientSecretStdin            bool
	flow, clientID, clientSecret, authURL, tokenURL, devURL string
	scopes                                                  []string
	redirectURL                                             string
	noBrowser                                               bool
	timeout                                                 time.Duration
}

func (a *App) loginCommand() *cobra.Command {
	var f loginFlags
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save credentials for this API (token, API key, basic auth or OAuth)",
		Long: fmt.Sprintf(`Save credentials for this API. They're stored in the OS keychain (or a
private file when no keychain is available) and used on every request.
Environment variables still take precedence (see "%[1]s auth status").

A login is bound to this API's trusted hosts (its server and the spec's
servers) and is never sent anywhere else, even with --server.

The credential type is inferred from the API's security schemes; use
--scheme to pick one when there are several, or the flags below to set
it explicitly.

Secrets are prompted for without echo. In scripts, pipe them in with
--token-stdin / --password-stdin / --client-secret-stdin rather than
passing them as flags, which end up in shell history.

OAuth2 flows:
  authorization_code  opens a browser to sign in (PKCE, local redirect)
  device_code         prints a URL and code to enter on any device
  client_credentials  machine-to-machine, needs a client secret
OAuth tokens are refreshed automatically when they expire.`, a.name),
		Example: fmt.Sprintf(`  %[1]s auth login                                     # prompt, type inferred from the spec
  echo "$TOKEN" | %[1]s auth login --token-stdin
  %[1]s auth login --header X-API-Key                  # API key in a custom header
  %[1]s auth login --header Authorization --prefix "Token "
  %[1]s auth login --type basic --username alice
  %[1]s auth login --type oauth2 --client-id abc        # browser login, URLs from the spec
  %[1]s auth login --type oauth2 --flow device_code --client-id abc --device-url https://idp/device --token-url https://idp/token
  %[1]s auth login --type oauth2 --flow client_credentials --client-id abc --client-secret-stdin < secret.txt`, a.name),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts := a.TrustedHosts()
			if len(hosts) == 0 {
				return fmt.Errorf("can't log in to %s: no server is configured to bind the login to (the spec has no absolute server URL). %s",
					a.profileLabel(), a.untrustedHint)
			}
			cred, err := a.buildCredential(cmd, &f)
			if err != nil {
				return err
			}
			cred.Hosts = hosts
			cred.Created = time.Now().UTC().Truncate(time.Second)
			if err := a.credStore.Set(a.credKey, cred); err != nil {
				return fmt.Errorf("saving credential: %w", err)
			}
			out := cmd.OutOrStdout()
			p := style.For(out)
			fmt.Fprintln(out, p.Success(fmt.Sprintf("Logged in to %s: %s", p.Bold(a.profileLabel()), cred.Summary())))
			fmt.Fprintln(out, p.Dim("  Stored in "+a.credStore.Name()+"."))
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.scheme, "scheme", "", "security scheme from the spec to log in for")
	fl.StringVar(&f.typ, "type", "", "credential type: bearer|header|query|basic|oauth2")
	fl.StringVar(&f.header, "header", "", "send the token in this header (implies --type header)")
	fl.StringVar(&f.prefix, "prefix", "", `text before the token in --header, e.g. "Token "`)
	fl.StringVar(&f.query, "query", "", "send the token as this query parameter (implies --type query)")
	fl.StringVar(&f.token, "token", "", "token or API key (prefer the prompt or --token-stdin)")
	fl.BoolVar(&f.tokenStdin, "token-stdin", false, "read the token from stdin")
	fl.StringVar(&f.username, "username", "", "username for basic auth")
	fl.BoolVar(&f.passwordStdin, "password-stdin", false, "read the basic auth password from stdin")
	fl.StringVar(&f.flow, "flow", "", "oauth2 flow: authorization_code|device_code|client_credentials")
	fl.StringVar(&f.clientID, "client-id", "", "oauth2 client ID")
	fl.StringVar(&f.clientSecret, "client-secret", "", "oauth2 client secret (prefer --client-secret-stdin)")
	fl.BoolVar(&f.clientSecretStdin, "client-secret-stdin", false, "read the oauth2 client secret from stdin")
	fl.StringVar(&f.authURL, "auth-url", "", "oauth2 authorization URL (default: from the spec)")
	fl.StringVar(&f.tokenURL, "token-url", "", "oauth2 token URL (default: from the spec)")
	fl.StringVar(&f.devURL, "device-url", "", "oauth2 device authorization URL")
	fl.StringArrayVar(&f.scopes, "scope", nil, "oauth2 scope to request (repeatable; default: scopes the API's operations need)")
	fl.StringVar(&f.redirectURL, "redirect-url", "", "oauth2 loopback redirect URL registered with the provider (default http://127.0.0.1:<random>/callback)")
	fl.BoolVar(&f.noBrowser, "no-browser", false, "print the login URL instead of opening a browser")
	fl.DurationVar(&f.timeout, "login-timeout", 5*time.Minute, "how long to wait for a browser or device login")
	cmd.RegisterFlagCompletionFunc("scheme", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return sortedKeys(a.api.SecuritySchemes), cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func (a *App) buildCredential(cmd *cobra.Command, f *loginFlags) (*auth.Credential, error) {
	out := cmd.ErrOrStderr()
	scheme, err := a.pickScheme(f, out)
	if err != nil {
		return nil, err
	}
	cred := &auth.Credential{}
	if scheme != nil {
		cred.Scheme = scheme.Name
	}

	// Explicit flags win over what the scheme suggests.
	switch {
	case f.header != "":
		cred.Type, cred.Name, cred.Prefix = auth.TypeHeader, f.header, f.prefix
	case f.query != "":
		cred.Type, cred.Name = auth.TypeQuery, f.query
	case f.typ != "":
		cred.Type = auth.CredentialType(f.typ)
	case scheme != nil:
		switch {
		case scheme.Type == "apiKey" && scheme.In == spec.InQuery:
			cred.Type, cred.Name = auth.TypeQuery, scheme.ParamName
		case scheme.Type == "apiKey" && scheme.In == spec.InCookie:
			cred.Type, cred.Name, cred.Prefix = auth.TypeHeader, "Cookie", scheme.ParamName+"="
		case scheme.Type == "apiKey":
			cred.Type, cred.Name = auth.TypeHeader, scheme.ParamName
		case scheme.Type == "http" && scheme.Scheme == "basic":
			cred.Type = auth.TypeBasic
		case scheme.Type == "oauth2" || scheme.Type == "openIdConnect":
			cred.Type = auth.TypeOAuth2
		default:
			cred.Type = auth.TypeBearer
		}
	default:
		cred.Type = auth.TypeBearer
	}
	if (cred.Type == auth.TypeHeader || cred.Type == auth.TypeQuery) && cred.Name == "" {
		return nil, fmt.Errorf("--type %s needs a name: use --header <name> or --query <name>", cred.Type)
	}

	stdin := cmd.InOrStdin()
	switch cred.Type {
	case auth.TypeBearer, auth.TypeHeader, auth.TypeQuery:
		cred.Token, err = secretInput(f.token, f.tokenStdin, stdin, out, "Token")
	case auth.TypeBasic:
		if cred.Username = f.username; cred.Username == "" {
			if cred.Username, err = prompt(out, "Username", false); err != nil {
				return nil, err
			}
		}
		cred.Password, err = secretInput("", f.passwordStdin, stdin, out, "Password")
	case auth.TypeOAuth2:
		cred.OAuth, err = a.oauthLogin(cmd, f, scheme, stdin, out)
	default:
		return nil, fmt.Errorf("unknown --type %q (bearer, header, query, basic, oauth2)", cred.Type)
	}
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// pickScheme chooses the spec security scheme to log in for.
func (a *App) pickScheme(f *loginFlags, out io.Writer) (*spec.SecurityScheme, error) {
	schemes := a.api.SecuritySchemes
	if f.scheme != "" {
		s, ok := schemes[f.scheme]
		if !ok {
			return nil, fmt.Errorf("unknown scheme %q; the API declares: %s", f.scheme, strings.Join(sortedKeys(schemes), ", "))
		}
		return s, nil
	}
	if len(schemes) == 0 {
		return nil, nil
	}
	// Prefer what explicit flags imply, then the most convenient type.
	want := func(s *spec.SecurityScheme) int {
		switch {
		case f.typ == string(auth.TypeOAuth2) || f.clientID != "":
			if s.Type == "oauth2" || s.Type == "openIdConnect" {
				return 0
			}
		case f.typ == string(auth.TypeBasic):
			if s.Type == "http" && s.Scheme == "basic" {
				return 0
			}
		case f.header != "" || f.query != "":
			if s.Type == "apiKey" {
				return 0
			}
		}
		switch {
		case s.Type == "http" && s.Scheme == "bearer":
			return 1
		case s.Type == "apiKey":
			return 2
		case s.Type == "oauth2" || s.Type == "openIdConnect":
			return 3
		}
		return 4
	}
	names := sortedKeys(schemes)
	sort.SliceStable(names, func(i, j int) bool { return want(schemes[names[i]]) < want(schemes[names[j]]) })
	chosen := schemes[names[0]]
	if len(names) > 1 {
		p := style.For(out)
		fmt.Fprintln(out, p.Info(fmt.Sprintf("Logging in for scheme %s (%s). %s",
			p.Bold(chosen.Name), chosen.Type, p.Dim("Others: "+strings.Join(names[1:], ", ")+"; choose with --scheme."))))
	}
	return chosen, nil
}

func (a *App) oauthLogin(cmd *cobra.Command, f *loginFlags, scheme *spec.SecurityScheme, stdin io.Reader, out io.Writer) (*auth.OAuthCredential, error) {
	ctx := cmd.Context()
	o := &auth.OAuthCredential{
		ClientID: f.clientID, ClientSecret: f.clientSecret,
		AuthURL: f.authURL, TokenURL: f.tokenURL, DeviceAuthURL: f.devURL,
		Scopes: f.scopes, Flow: auth.OAuthFlow(f.flow),
	}

	// Fill gaps from the spec.
	if scheme != nil && scheme.Flows != nil {
		if ac := scheme.Flows.AuthorizationCode; ac != nil {
			o.AuthURL = firstNonEmpty(o.AuthURL, ac.AuthorizationURL)
			if o.Flow == "" || o.Flow == auth.FlowAuthorizationCode || o.Flow == auth.FlowDeviceCode {
				o.TokenURL = firstNonEmpty(o.TokenURL, ac.TokenURL)
			}
		}
		if cc := scheme.Flows.ClientCredentials; cc != nil && (o.Flow == "" || o.Flow == auth.FlowClientCredentials) {
			o.TokenURL = firstNonEmpty(o.TokenURL, cc.TokenURL)
		}
	}
	if scheme != nil && scheme.Type == "openIdConnect" && scheme.OpenIDConnectURL != "" {
		disc, err := auth.Discover(ctx, a.httpClient, scheme.OpenIDConnectURL)
		if err != nil {
			return nil, err
		}
		o.AuthURL = firstNonEmpty(o.AuthURL, disc.AuthorizationEndpoint)
		o.TokenURL = firstNonEmpty(o.TokenURL, disc.TokenEndpoint)
		o.DeviceAuthURL = firstNonEmpty(o.DeviceAuthURL, disc.DeviceAuthorizationEndpoint)
		if len(o.Scopes) == 0 {
			o.Scopes = []string{"openid"}
		}
	}
	if len(o.Scopes) == 0 && scheme != nil {
		o.Scopes = a.requiredScopes(scheme.Name)
	}

	if o.Flow == "" {
		switch {
		case o.AuthURL != "" && !f.noBrowser:
			o.Flow = auth.FlowAuthorizationCode
		case o.DeviceAuthURL != "":
			o.Flow = auth.FlowDeviceCode
		case o.AuthURL != "":
			o.Flow = auth.FlowAuthorizationCode
		case o.TokenURL != "":
			o.Flow = auth.FlowClientCredentials
		default:
			return nil, errors.New("no OAuth endpoints found in the spec; pass --auth-url/--token-url (browser), --device-url/--token-url (device) or --token-url (client credentials)")
		}
	}
	if o.TokenURL == "" {
		return nil, errors.New("missing OAuth token URL; pass --token-url")
	}

	var err error
	if o.ClientID == "" {
		if o.ClientID, err = prompt(out, "OAuth client ID", false); err != nil {
			return nil, err
		}
	}
	if f.clientSecretStdin {
		if o.ClientSecret, err = readLine(stdin); err != nil {
			return nil, err
		}
	} else if o.ClientSecret == "" && o.Flow == auth.FlowClientCredentials {
		if o.ClientSecret, err = prompt(out, "OAuth client secret", true); err != nil {
			return nil, err
		}
	}
	if a.httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, a.httpClient)
	}

	switch o.Flow {
	case auth.FlowAuthorizationCode:
		o.Token, err = auth.LoginAuthorizationCode(ctx, o, auth.BrowserOptions{
			RedirectURL: f.redirectURL, NoBrowser: f.noBrowser, Timeout: f.timeout, Out: out,
		})
	case auth.FlowDeviceCode:
		dctx, cancel := context.WithTimeout(ctx, f.timeout)
		defer cancel()
		o.Token, err = auth.LoginDeviceCode(dctx, o, out)
	case auth.FlowClientCredentials:
		o.Token, err = auth.LoginClientCredentials(ctx, o)
	default:
		return nil, fmt.Errorf("unknown --flow %q (authorization_code, device_code, client_credentials)", o.Flow)
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

// requiredScopes collects the scopes exposed operations need for scheme.
func (a *App) requiredScopes(scheme string) []string {
	seen := map[string]bool{}
	var out []string
	for _, op := range a.ops {
		for _, req := range op.Security {
			for _, s := range req[scheme] {
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) logoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the stored credentials for this API",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := a.credStore.Delete(a.credKey)
			if errors.Is(err, auth.ErrNotFound) {
				fmt.Fprintln(cmd.OutOrStdout(), style.For(cmd.OutOrStdout()).Info("No stored credentials for "+a.profileLabel()+"."))
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), style.For(cmd.OutOrStdout()).Success("Logged out of "+a.profileLabel()+"; stored credentials deleted."))
			return nil
		},
	}
}

func (a *App) tokenCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the stored access token, refreshing it if needed",
		Long:  "Print the stored token (refreshing OAuth tokens first) to stdout, e.g. for use with curl. This prints a secret.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := &auth.StoredProvider{Store: a.credStore, Key: a.credKey, HTTPClient: a.httpClient}
			tok, err := p.Token(cmd.Context())
			if errors.Is(err, auth.ErrNotFound) {
				return fmt.Errorf("no stored credentials; run \"%s auth login\"", a.name)
			}
			if err != nil {
				return err
			}
			if tok.AccessToken == "" {
				return errors.New("the stored credential has no token (basic auth?)")
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok.AccessToken)
			return nil
		},
	}
}

// --- input helpers --------------------------------------------------------------

// secretInput returns the flag value, else stdin when fromStdin, else a
// hidden prompt on the terminal.
func secretInput(flagVal string, fromStdin bool, stdin io.Reader, out io.Writer, label string) (string, error) {
	switch {
	case flagVal != "":
		return flagVal, nil
	case fromStdin:
		v, err := readLine(stdin)
		if err == nil && v == "" {
			err = fmt.Errorf("%s from stdin is empty", strings.ToLower(label))
		}
		return v, err
	}
	return prompt(out, label, true)
}

func prompt(out io.Writer, label string, secret bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("%s required: no terminal to prompt on, so pass it with a flag or via stdin", strings.ToLower(label))
	}
	fmt.Fprintf(out, "%s ", style.For(out).Bold(label+":"))
	var (
		v   string
		err error
	)
	if secret {
		var b []byte
		b, err = term.ReadPassword(fd)
		fmt.Fprintln(out)
		v = string(b)
	} else {
		v, err = readLine(os.Stdin)
	}
	v = strings.TrimSpace(v)
	if err == nil && v == "" {
		err = fmt.Errorf("%s can't be empty", strings.ToLower(label))
	}
	return v, err
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// memoStore caches reads for the life of the process: one request may consult
// the stored credential once per security scheme, and keychain reads are slow.
type memoStore struct {
	auth.Store
	mu    sync.Mutex
	cache map[string]*auth.Credential
	err   map[string]error
}

func cachedStore(s auth.Store) auth.Store {
	return &memoStore{Store: s, cache: map[string]*auth.Credential{}, err: map[string]error{}}
}

func (m *memoStore) Get(key string) (*auth.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cache[key]; ok {
		return c, m.err[key]
	}
	c, err := m.Store.Get(key)
	m.cache[key], m.err[key] = c, err
	return c, err
}

func (m *memoStore) Set(key string, c *auth.Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.Store.Set(key, c)
	if err == nil {
		m.cache[key], m.err[key] = c, nil
	}
	return err
}

func (m *memoStore) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, key)
	delete(m.err, key)
	return m.Store.Delete(key)
}

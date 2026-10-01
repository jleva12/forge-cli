package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// StoredProvider applies the credential saved under Key, refreshing OAuth
// tokens when they expire and saving the new token back to the store.
type StoredProvider struct {
	Store Store
	Key   string
	// HTTPClient is used for token requests. Defaults to http.DefaultClient.
	HTTPClient *http.Client
}

func (p *StoredProvider) Apply(ctx context.Context, req *http.Request) error {
	c, err := p.Store.Get(p.Key)
	if errors.Is(err, ErrNotFound) {
		return ErrNoCredentials
	}
	if err != nil {
		return fmt.Errorf("reading stored credential: %w", err)
	}
	if len(c.Hosts) > 0 && !HostAllowed(req.URL, c.Hosts) {
		return fmt.Errorf("the stored login is bound to %s, not %s; run \"auth login\" again to use this server",
			strings.Join(c.Hosts, ", "), req.URL.Host)
	}
	switch c.Type {
	case TypeBearer:
		req.Header.Set("Authorization", "Bearer "+c.Token)
	case TypeHeader:
		req.Header.Set(c.Name, c.Prefix+c.Token)
	case TypeQuery:
		q := req.URL.Query()
		q.Set(c.Name, c.Token)
		req.URL.RawQuery = q.Encode()
	case TypeBasic:
		req.SetBasicAuth(c.Username, c.Password)
	case TypeOAuth2:
		tok, err := p.oauthToken(ctx, c)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", tok.Type()+" "+tok.AccessToken)
	default:
		return fmt.Errorf("unknown stored credential type %q", c.Type)
	}
	return nil
}

func (p *StoredProvider) Describe() string {
	c, err := p.Store.Get(p.Key)
	if err != nil {
		return "nothing stored (run \"auth login\")"
	}
	return c.Summary() + " in " + p.Store.Name()
}

// Token returns a valid access token for an OAuth credential, refreshing it
// if needed.
func (p *StoredProvider) Token(ctx context.Context) (*oauth2.Token, error) {
	c, err := p.Store.Get(p.Key)
	if err != nil {
		return nil, err
	}
	if c.Type != TypeOAuth2 {
		return &oauth2.Token{AccessToken: c.Token, TokenType: "Bearer"}, nil
	}
	return p.oauthToken(ctx, c)
}

func (p *StoredProvider) oauthToken(ctx context.Context, c *Credential) (*oauth2.Token, error) {
	o := c.OAuth
	if o == nil {
		return nil, errors.New("stored oauth2 credential is incomplete; run \"auth login\" again")
	}
	if o.Token != nil && o.Token.Valid() {
		return o.Token, nil
	}
	if p.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, p.HTTPClient)
	}
	var (
		tok *oauth2.Token
		err error
	)
	switch {
	case o.Flow == FlowClientCredentials:
		tok, err = o.clientCredentials().Token(ctx)
	case o.Token != nil && o.Token.RefreshToken != "":
		tok, err = o.Config("").TokenSource(ctx, o.Token).Token()
	default:
		return nil, errors.New("oauth2 token expired and can't be refreshed; run \"auth login\" again")
	}
	if err != nil {
		return nil, fmt.Errorf("refreshing oauth2 token (run \"auth login\" again if this persists): %w", err)
	}
	// Some providers omit the refresh token on refresh; keep the old one.
	if tok.RefreshToken == "" && o.Token != nil {
		tok.RefreshToken = o.Token.RefreshToken
	}
	o.Token = tok
	_ = p.Store.Set(p.Key, c) // best effort: the token is still usable this run
	return tok, nil
}

// Config builds the oauth2 configuration. redirectURL is only needed for the
// authorization code flow.
func (o *OAuthCredential) Config(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Scopes:       o.Scopes,
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:       o.AuthURL,
			TokenURL:      o.TokenURL,
			DeviceAuthURL: o.DeviceAuthURL,
		},
	}
}

func (o *OAuthCredential) clientCredentials() *clientcredentials.Config {
	return &clientcredentials.Config{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret, TokenURL: o.TokenURL, Scopes: o.Scopes,
	}
}

// --- Login flows ----------------------------------------------------------------

// LoginClientCredentials fetches a token with the client credentials grant.
func LoginClientCredentials(ctx context.Context, o *OAuthCredential) (*oauth2.Token, error) {
	if o.ClientSecret == "" {
		return nil, errors.New("the client credentials flow needs a client secret")
	}
	return o.clientCredentials().Token(ctx)
}

// LoginDeviceCode runs the device authorization grant (RFC 8628): the user
// opens a URL on any device and enters a code.
func LoginDeviceCode(ctx context.Context, o *OAuthCredential, out io.Writer) (*oauth2.Token, error) {
	if o.DeviceAuthURL == "" {
		return nil, errors.New("the device code flow needs a device authorization URL (--device-url)")
	}
	cfg := o.Config("")
	resp, err := cfg.DeviceAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting device login: %w", err)
	}
	fmt.Fprintf(out, "To sign in, open %s and enter the code: %s\n", resp.VerificationURI, resp.UserCode)
	if resp.VerificationURIComplete != "" {
		fmt.Fprintf(out, "Or open: %s\n", resp.VerificationURIComplete)
	}
	fmt.Fprintln(out, "Waiting for approval...")
	return cfg.DeviceAccessToken(ctx, resp)
}

// BrowserOptions configures the authorization code flow.
type BrowserOptions struct {
	// RedirectURL is the loopback callback, e.g. http://127.0.0.1:8085/callback.
	// Port 0 picks a free port. It must match what's registered with the
	// OAuth provider. Default: http://127.0.0.1:0/callback.
	RedirectURL string
	// NoBrowser prints the URL instead of opening a browser.
	NoBrowser bool
	// Timeout bounds how long to wait for the user (default 5 minutes).
	Timeout time.Duration
	Out     io.Writer
}

// LoginAuthorizationCode runs the authorization code flow with PKCE, using a
// temporary local server to receive the redirect (RFC 8252).
func LoginAuthorizationCode(ctx context.Context, o *OAuthCredential, opts BrowserOptions) (*oauth2.Token, error) {
	if o.AuthURL == "" {
		return nil, errors.New("the authorization code flow needs an authorization URL (--auth-url)")
	}
	if opts.RedirectURL == "" {
		opts.RedirectURL = "http://127.0.0.1:0/callback"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Minute
	}
	redirect, err := url.Parse(opts.RedirectURL)
	if err != nil || redirect.Scheme != "http" {
		return nil, fmt.Errorf("redirect URL must be an http loopback URL, got %q", opts.RedirectURL)
	}
	ln, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return nil, fmt.Errorf("starting local callback server on %s: %w", redirect.Host, err)
	}
	defer ln.Close()
	redirect.Host = fmt.Sprintf("%s:%d", redirect.Hostname(), ln.Addr().(*net.TCPAddr).Port)
	if redirect.Path == "" {
		redirect.Path = "/"
	}

	cfg := o.Config(redirect.String())
	verifier := oauth2.GenerateVerifier()
	state := randomString()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != redirect.Path {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("login failed: state mismatch (possible CSRF); try again")
		case q.Get("error") != "":
			res.err = fmt.Errorf("login failed: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("login failed: no authorization code in the redirect")
		default:
			res.code = q.Get("code")
		}
		msg := "Login complete. You can close this tab and return to the terminal."
		if res.err != nil {
			msg = res.err.Error()
		}
		fmt.Fprintf(w, "<!doctype html><title>Login</title><p style=\"font:16px sans-serif;margin:3em\">%s</p>", html.EscapeString(msg))
		select {
		case results <- res:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	if opts.NoBrowser || openBrowser(authURL) != nil {
		fmt.Fprintf(opts.Out, "Open this URL in your browser to sign in:\n\n  %s\n\n", authURL)
	} else {
		fmt.Fprintf(opts.Out, "Opening your browser to sign in. If it doesn't open, visit:\n\n  %s\n\n", authURL)
	}
	fmt.Fprintf(opts.Out, "Waiting for the redirect to %s ...\n", redirect.String())

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	select {
	case res := <-results:
		if res.err != nil {
			return nil, res.err
		}
		return cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for the browser login after %s", opts.Timeout)
	}
}

// --- OpenID Connect discovery --------------------------------------------------

// OIDCConfig is the subset of an OpenID Connect discovery document forge uses.
type OIDCConfig struct {
	AuthorizationEndpoint       string   `json:"authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	ScopesSupported             []string `json:"scopes_supported"`
}

// Discover fetches an OpenID Connect discovery document. issuer may be the
// issuer URL or the full .well-known/openid-configuration URL.
func Discover(ctx context.Context, client *http.Client, issuer string) (*OIDCConfig, error) {
	if client == nil {
		client = http.DefaultClient
	}
	u := issuer
	if !strings.Contains(u, "/.well-known/") {
		u = strings.TrimRight(u, "/") + "/.well-known/openid-configuration"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery %s: %s", u, resp.Status)
	}
	var cfg OIDCConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("OIDC discovery %s: %w", u, err)
	}
	return &cfg, nil
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

// Chain tries providers in order and uses the first one with credentials,
// e.g. an environment variable first and a stored login second.
func Chain(providers ...Provider) Provider { return chain(providers) }

type chain []Provider

func (c chain) Apply(ctx context.Context, req *http.Request) error {
	for _, p := range c {
		err := p.Apply(ctx, req)
		if err == nil || !errors.Is(err, ErrNoCredentials) {
			return err
		}
	}
	return ErrNoCredentials
}

func (c chain) Describe() string {
	var parts []string
	for _, p := range c {
		if d, ok := p.(Describer); ok {
			parts = append(parts, d.Describe())
		}
	}
	return strings.Join(parts, "; else ")
}

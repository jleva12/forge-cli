package auth

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeIdP is a minimal OAuth2 server: authorize (with PKCE), token
// (authorization_code, refresh_token, client_credentials, device_code) and
// device authorization endpoints.
type fakeIdP struct {
	*httptest.Server
	mu        sync.Mutex
	challenge string
	grants    []string
}

func newIdP(t *testing.T) *fakeIdP {
	idp := &fakeIdP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idp.mu.Lock()
		idp.challenge = q.Get("code_challenge")
		idp.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=the-code&state="+q.Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev", "user_code": "ABCD-EFGH", "verification_uri": "https://idp.test/activate",
			"interval": 1, "expires_in": 60,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		grant := r.Form.Get("grant_type")
		idp.mu.Lock()
		idp.grants = append(idp.grants, grant)
		challenge := idp.challenge
		idp.mu.Unlock()
		tok := map[string]any{"token_type": "Bearer", "expires_in": 3600}
		switch grant {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			tok["access_token"], tok["refresh_token"] = "access-1", "refresh-1"
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			tok["access_token"] = "access-2" // no new refresh token: the old one must be kept
		case "client_credentials":
			id, secret, ok := r.BasicAuth()
			if !ok {
				id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
			if id != "cid" || secret != "csecret" {
				http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
				return
			}
			tok["access_token"] = "access-cc"
		case "urn:ietf:params:oauth:grant-type:device_code":
			tok["access_token"], tok["refresh_token"] = "access-dev", "refresh-dev"
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tok)
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

func (idp *fakeIdP) oauth(flow OAuthFlow) *OAuthCredential {
	return &OAuthCredential{
		Flow: flow, ClientID: "cid", ClientSecret: "csecret",
		AuthURL: idp.URL + "/authorize", TokenURL: idp.URL + "/token", DeviceAuthURL: idp.URL + "/device",
	}
}

func TestAuthorizationCodeWithPKCE(t *testing.T) {
	idp := newIdP(t)
	pr, pw := io.Pipe()
	// Play the browser: read the printed URL and follow it.
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); strings.HasPrefix(line, idp.URL+"/authorize") {
				resp, err := http.Get(line)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
	}()
	tok, err := LoginAuthorizationCode(context.Background(), idp.oauth(FlowAuthorizationCode),
		BrowserOptions{NoBrowser: true, Out: pw, Timeout: 10 * time.Second})
	pw.Close()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" {
		t.Errorf("token = %+v", tok)
	}
}

func TestDeviceCodeAndClientCredentials(t *testing.T) {
	idp := newIdP(t)
	var out strings.Builder
	tok, err := LoginDeviceCode(context.Background(), idp.oauth(FlowDeviceCode), &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access-dev" || !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Errorf("device: token=%+v out=%q", tok, out.String())
	}

	tok, err = LoginClientCredentials(context.Background(), idp.oauth(FlowClientCredentials))
	if err != nil || tok.AccessToken != "access-cc" {
		t.Errorf("client credentials: %v %+v", err, tok)
	}
	bad := idp.oauth(FlowClientCredentials)
	bad.ClientSecret = "wrong"
	if _, err := LoginClientCredentials(context.Background(), bad); err == nil {
		t.Error("wrong client secret should fail")
	}
}

func TestStoredProviderTypes(t *testing.T) {
	store := &FileStore{Path: filepath.Join(t.TempDir(), "creds.json")}
	p := &StoredProvider{Store: store, Key: "k"}
	apply := func() *http.Request {
		t.Helper()
		req, _ := http.NewRequest("GET", "https://api.test/x", nil)
		if err := p.Apply(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		return req
	}

	req, _ := http.NewRequest("GET", "https://api.test/x", nil)
	if err := p.Apply(context.Background(), req); err != ErrNoCredentials {
		t.Errorf("empty store: err = %v, want ErrNoCredentials", err)
	}

	store.Set("k", &Credential{Type: TypeBearer, Token: "t1"})
	if got := apply().Header.Get("Authorization"); got != "Bearer t1" {
		t.Errorf("bearer: %q", got)
	}
	store.Set("k", &Credential{Type: TypeHeader, Name: "X-Api-Key", Prefix: "Key ", Token: "t2"})
	if got := apply().Header.Get("X-Api-Key"); got != "Key t2" {
		t.Errorf("header: %q", got)
	}
	store.Set("k", &Credential{Type: TypeQuery, Name: "api_key", Token: "t3"})
	if got := apply().URL.Query().Get("api_key"); got != "t3" {
		t.Errorf("query: %q", got)
	}
	store.Set("k", &Credential{Type: TypeBasic, Username: "u", Password: "p"})
	if u, pw, _ := apply().BasicAuth(); u != "u" || pw != "p" {
		t.Errorf("basic: %q %q", u, pw)
	}
}

func TestStoredProviderRefreshesAndPersists(t *testing.T) {
	idp := newIdP(t)
	store := &FileStore{Path: filepath.Join(t.TempDir(), "creds.json")}
	o := idp.oauth(FlowAuthorizationCode)
	o.Token = &oauth2.Token{AccessToken: "access-1", RefreshToken: "refresh-1", Expiry: time.Now().Add(-time.Minute)}
	store.Set("k", &Credential{Type: TypeOAuth2, OAuth: o})

	p := &StoredProvider{Store: store, Key: "k"}
	req, _ := http.NewRequest("GET", "https://api.test/x", nil)
	if err := p.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer access-2" {
		t.Errorf("Authorization = %q, want refreshed token", got)
	}
	saved, _ := store.Get("k")
	if saved.OAuth.Token.AccessToken != "access-2" || saved.OAuth.Token.RefreshToken != "refresh-1" {
		t.Errorf("persisted token = %+v (refresh token must be kept)", saved.OAuth.Token)
	}

	// Client credentials re-fetch instead of refreshing.
	cc := idp.oauth(FlowClientCredentials)
	cc.Token = &oauth2.Token{AccessToken: "old", Expiry: time.Now().Add(-time.Minute)}
	store.Set("k", &Credential{Type: TypeOAuth2, OAuth: cc})
	req, _ = http.NewRequest("GET", "https://api.test/x", nil)
	if err := p.Apply(context.Background(), req); err != nil || req.Header.Get("Authorization") != "Bearer access-cc" {
		t.Errorf("client credentials refresh: %v %q", err, req.Header.Get("Authorization"))
	}

	// An expired token without a refresh token asks for a new login.
	store.Set("k", &Credential{Type: TypeOAuth2, OAuth: &OAuthCredential{
		Flow: FlowAuthorizationCode, TokenURL: idp.URL + "/token",
		Token: &oauth2.Token{AccessToken: "x", Expiry: time.Now().Add(-time.Minute)},
	}})
	if err := p.Apply(context.Background(), req); err == nil || !strings.Contains(err.Error(), "auth login") {
		t.Errorf("expected re-login error, got %v", err)
	}
}

func TestFileStorePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "creds.json")
	store := &FileStore{Path: path}
	if err := store.Set("k", &Credential{Type: TypeBearer, Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v, want 0600", info.Mode().Perm())
	}
	if err := store.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("k"); err != ErrNotFound {
		t.Errorf("after delete: %v", err)
	}
}

func TestMask(t *testing.T) {
	if got := Mask("sk_live_abcdef123456"); got != "••••3456" {
		t.Errorf("Mask = %q", got)
	}
	if got := Mask("short"); strings.Contains(got, "s") {
		t.Errorf("short secrets must be fully masked: %q", got)
	}
}

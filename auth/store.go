package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

// Credential is a stored login. Exactly one kind of secret is set,
// according to Type.
type Credential struct {
	Type CredentialType `json:"type"`
	// Scheme is the spec security scheme this credential satisfies. Empty
	// means it applies to any operation needing auth.
	Scheme string `json:"scheme,omitempty"`

	// Token is the secret for bearer, header and query credentials.
	Token string `json:"token,omitempty"`
	// Name is the header or query parameter name for header/query credentials.
	Name string `json:"name,omitempty"`
	// Prefix is prepended to the token for header credentials, e.g. "Token ".
	Prefix string `json:"prefix,omitempty"`

	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	OAuth *OAuthCredential `json:"oauth,omitempty"`

	// Hosts the credential may be sent to, recorded at login. Empty means
	// unrestricted (only for credentials saved by code, not by login).
	Hosts []string `json:"hosts,omitempty"`

	Created time.Time `json:"created"`
}

type CredentialType string

const (
	TypeBearer CredentialType = "bearer"
	TypeHeader CredentialType = "header"
	TypeQuery  CredentialType = "query"
	TypeBasic  CredentialType = "basic"
	TypeOAuth2 CredentialType = "oauth2"
)

// OAuthCredential holds an OAuth2 client configuration and its current token.
type OAuthCredential struct {
	Flow          OAuthFlow     `json:"flow"`
	ClientID      string        `json:"client_id"`
	ClientSecret  string        `json:"client_secret,omitempty"`
	AuthURL       string        `json:"auth_url,omitempty"`
	TokenURL      string        `json:"token_url"`
	DeviceAuthURL string        `json:"device_auth_url,omitempty"`
	Scopes        []string      `json:"scopes,omitempty"`
	Token         *oauth2.Token `json:"token,omitempty"`
}

type OAuthFlow string

const (
	FlowAuthorizationCode OAuthFlow = "authorization_code"
	FlowDeviceCode        OAuthFlow = "device_code"
	FlowClientCredentials OAuthFlow = "client_credentials"
)

// Summary describes the credential without revealing secrets.
func (c *Credential) Summary() string {
	s := c.kindSummary()
	if len(c.Hosts) > 0 {
		s += " for " + strings.Join(c.Hosts, ", ")
	}
	return s
}

func (c *Credential) kindSummary() string {
	switch c.Type {
	case TypeBearer:
		return "bearer token " + Mask(c.Token)
	case TypeHeader:
		return fmt.Sprintf("header %s: %s%s", c.Name, c.Prefix, Mask(c.Token))
	case TypeQuery:
		return fmt.Sprintf("query parameter %s=%s", c.Name, Mask(c.Token))
	case TypeBasic:
		return "basic auth as " + c.Username
	case TypeOAuth2:
		if c.OAuth == nil {
			return "oauth2 (incomplete)"
		}
		s := fmt.Sprintf("oauth2 %s, client %s", c.OAuth.Flow, c.OAuth.ClientID)
		if t := c.OAuth.Token; t != nil {
			switch {
			case t.Expiry.IsZero():
				s += ", token does not expire"
			case t.Expiry.After(time.Now()):
				s += ", token expires " + t.Expiry.Local().Format(time.RFC822)
			case t.RefreshToken != "" || c.OAuth.Flow == FlowClientCredentials:
				s += ", token expired (refreshes automatically)"
			default:
				s += ", token expired (run auth login again)"
			}
		}
		return s
	}
	return string(c.Type)
}

// Mask shows only the last four characters of a secret.
func Mask(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return "••••" + s[len(s)-4:]
}

// ErrNotFound is returned by a Store when no credential is saved for a key.
var ErrNotFound = errors.New("no stored credential")

// Store persists credentials by key (typically the CLI or API name).
type Store interface {
	Get(key string) (*Credential, error)
	Set(key string, c *Credential) error
	Delete(key string) error
	// Name describes the storage backend, e.g. "macOS Keychain".
	Name() string
}

// --- OS keychain ---------------------------------------------------------------

const keyringService = "forge-cli"

// KeyringStore keeps credentials in the OS keychain (macOS Keychain, Windows
// Credential Manager, or the Secret Service on Linux).
type KeyringStore struct{}

func (KeyringStore) Get(key string) (*Credential, error) {
	data, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeCredential(data)
}

func (KeyringStore) Set(key string, c *Credential) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return keyring.Set(keyringService, key, string(data))
}

func (KeyringStore) Delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func (KeyringStore) Name() string { return "OS keychain" }

// --- File ----------------------------------------------------------------------

// FileStore keeps credentials in a JSON file readable only by the current
// user. It's the fallback when no OS keychain is available.
type FileStore struct {
	Path string
	mu   sync.Mutex
}

func (f *FileStore) Get(key string) (*Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return nil, err
	}
	c, ok := all[key]
	if !ok {
		return nil, ErrNotFound
	}
	return c, nil
}

func (f *FileStore) Set(key string, c *Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return err
	}
	all[key] = c
	return f.write(all)
}

func (f *FileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	all, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := all[key]; !ok {
		return ErrNotFound
	}
	delete(all, key)
	return f.write(all)
}

func (f *FileStore) Name() string { return "file " + f.Path }

func (f *FileStore) read() (map[string]*Credential, error) {
	all := map[string]*Credential{}
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", f.Path, err)
	}
	return all, nil
}

func (f *FileStore) write(all map[string]*Credential) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func decodeCredential(data string) (*Credential, error) {
	var c Credential
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, fmt.Errorf("corrupt stored credential: %w", err)
	}
	return &c, nil
}

// --- Default -----------------------------------------------------------------

// DefaultStore uses the OS keychain when it works and falls back to
// ~/.config/forge/credentials.json (mode 0600). Set FORGE_CREDENTIAL_STORE
// to "file" or "keychain" to force one.
func DefaultStore() Store {
	file := &FileStore{Path: defaultCredentialsPath()}
	switch os.Getenv("FORGE_CREDENTIAL_STORE") {
	case "file":
		return file
	case "keychain":
		return KeyringStore{}
	}
	return &fallbackStore{primary: KeyringStore{}, fallback: file}
}

func defaultCredentialsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "forge", "credentials.json")
}

// fallbackStore reads from both stores and writes to the keychain, using the
// file only when the keychain is unavailable.
type fallbackStore struct {
	primary, fallback Store
}

func (s *fallbackStore) Get(key string) (*Credential, error) {
	c, err := s.primary.Get(key)
	if err == nil {
		return c, nil
	}
	return s.fallback.Get(key)
}

func (s *fallbackStore) Set(key string, c *Credential) error {
	if err := s.primary.Set(key, c); err == nil {
		// Don't leave a stale copy behind in the file.
		s.fallback.Delete(key)
		return nil
	}
	return s.fallback.Set(key, c)
}

func (s *fallbackStore) Delete(key string) error {
	errP, errF := s.primary.Delete(key), s.fallback.Delete(key)
	if errP == nil || errF == nil {
		return nil
	}
	return ErrNotFound
}

func (s *fallbackStore) Name() string {
	if _, err := s.primary.Get("__forge_probe__"); err == nil || errors.Is(err, ErrNotFound) {
		return s.primary.Name()
	}
	return s.fallback.Name()
}

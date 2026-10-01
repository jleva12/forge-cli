package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"forge-cli/filter"
	"forge-cli/forge"
	"forge-cli/naming"
)

// Registry is the set of named APIs forge knows about, stored as TOML so it
// can be edited by hand and shared:
//
//	active = "petstore"
//
//	[apis.petstore]
//	spec = "https://petstore3.swagger.io/api/v3/openapi.json"
//	exclude_tags = ["admin"]
//
//	[apis.billing]
//	spec = "/Users/me/specs/billing.yaml"
//	server = "https://billing.internal.example.com"
//	read_only = true
type Registry struct {
	Active string                `toml:"active,omitempty"`
	APIs   map[string]*APIConfig `toml:"apis"`
}

// APIConfig describes one registered API and how to shape its commands.
type APIConfig struct {
	Spec        string `toml:"spec"`
	Title       string `toml:"title,omitempty"`
	Description string `toml:"description,omitempty"`
	Server      string `toml:"server,omitempty"`
	EnvPrefix   string `toml:"env_prefix,omitempty"`
	// TrustedHosts may receive credentials in addition to server and the
	// spec's servers, e.g. a staging host used with --server.
	TrustedHosts []string  `toml:"trusted_hosts,omitempty"`
	Added        time.Time `toml:"added,omitempty"`

	// Groups are the generated command groups (the first tag, or the first
	// path segment for untagged specs), so they work for every spec.
	IncludeGroups     []string `toml:"include_groups,omitempty"`
	ExcludeGroups     []string `toml:"exclude_groups,omitempty"`
	IncludeTags       []string `toml:"include_tags,omitempty"`
	ExcludeTags       []string `toml:"exclude_tags,omitempty"`
	IncludePaths      []string `toml:"include_paths,omitempty"`
	ExcludePaths      []string `toml:"exclude_paths,omitempty"`
	ExcludeOperations []string `toml:"exclude_operations,omitempty"`
	ExcludeDeprecated bool     `toml:"exclude_deprecated,omitempty"`
	ReadOnly          bool     `toml:"read_only,omitempty"`
}

// envPrefix is used for credentials and server overrides, e.g. PETSTORE_TOKEN.
func (c *APIConfig) envPrefix(name string) string {
	if c.EnvPrefix != "" {
		return c.EnvPrefix
	}
	return naming.EnvName(name)
}

// options turns the config into forge options.
func (c *APIConfig) options(name string) []forge.Option {
	opts := []forge.Option{forge.WithSpec(c.Spec), forge.WithEnvPrefix(c.envPrefix(name))}
	if c.Server != "" {
		opts = append(opts, forge.WithBaseURL(c.Server))
	}
	if len(c.TrustedHosts) > 0 {
		opts = append(opts, forge.WithTrustedHosts(c.TrustedHosts...))
	}
	var fs []filter.Filter
	if len(c.IncludeGroups) > 0 {
		fs = append(fs, filter.Include(filter.Group(c.IncludeGroups...)))
	}
	if len(c.ExcludeGroups) > 0 {
		fs = append(fs, filter.Exclude(filter.Group(c.ExcludeGroups...)))
	}
	if len(c.IncludeTags) > 0 {
		fs = append(fs, filter.IncludeTags(c.IncludeTags...))
	}
	if len(c.ExcludeTags) > 0 {
		fs = append(fs, filter.ExcludeTags(c.ExcludeTags...))
	}
	if len(c.IncludePaths) > 0 {
		fs = append(fs, filter.IncludePaths(c.IncludePaths...))
	}
	if len(c.ExcludePaths) > 0 {
		fs = append(fs, filter.ExcludePaths(c.ExcludePaths...))
	}
	if len(c.ExcludeOperations) > 0 {
		fs = append(fs, filter.ExcludeOperations(c.ExcludeOperations...))
	}
	if c.ExcludeDeprecated {
		fs = append(fs, filter.ExcludeDeprecated())
	}
	if c.ReadOnly {
		fs = append(fs, filter.ReadOnly())
	}
	if len(fs) > 0 {
		opts = append(opts, forge.WithFilters(fs...))
	}
	return opts
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid API name %q: use letters, digits, - and _ (max 64 characters)", name)
	}
	return nil
}

// registryPath is $FORGE_CONFIG, else $XDG_CONFIG_HOME/forge/apis.toml,
// else ~/.config/forge/apis.toml.
func registryPath() (string, error) {
	if p := os.Getenv("FORGE_CONFIG"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "forge", "apis.toml"), nil
}

func loadRegistry() (*Registry, error) {
	path, err := registryPath()
	if err != nil {
		return nil, err
	}
	r, err := readRegistryFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{APIs: map[string]*APIConfig{}}, nil
	}
	return r, err
}

func readRegistryFile(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r := &Registry{}
	if _, err := toml.Decode(string(data), r); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if r.APIs == nil {
		r.APIs = map[string]*APIConfig{}
	}
	return r, nil
}

func (r *Registry) save() (string, error) {
	path, err := registryPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	data, err := r.encode()
	if err != nil {
		return "", err
	}
	// Write atomically so a failed write never corrupts the registry.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func (r *Registry) encode() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# forge API registry. Edit by hand or manage with: forge add | use | apis | remove\n\n")
	if err := toml.NewEncoder(&buf).Encode(r); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r *Registry) names() []string {
	names := make([]string, 0, len(r.APIs))
	for n := range r.APIs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// lookup finds an API by name, suggesting near matches when it's missing.
func (r *Registry) lookup(name string) (*APIConfig, error) {
	if c, ok := r.APIs[name]; ok {
		return c, nil
	}
	if len(r.APIs) == 0 {
		return nil, fmt.Errorf("no API named %q; no APIs are registered yet (add one with \"forge add <name> <spec>\")", name)
	}
	return nil, fmt.Errorf("no API named %q; registered APIs: %s", name, strings.Join(r.names(), ", "))
}

// endpoint is what a login is bound to; changing it invalidates the login.
func (c *APIConfig) endpoint() string {
	return c.Spec + "|" + c.Server + "|" + strings.Join(c.TrustedHosts, ",")
}

// envPrefixConflict returns the name of another API using the same
// environment variable prefix, which would make them share credentials.
func (r *Registry) envPrefixConflict(name string, c *APIConfig) string {
	prefix := c.envPrefix(name)
	for _, other := range r.names() {
		if other != name && r.APIs[other].envPrefix(other) == prefix {
			return other
		}
	}
	return ""
}

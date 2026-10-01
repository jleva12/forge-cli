// Package forge turns an OpenAPI document into a Cobra CLI.
//
// The pipeline is:
//
//	load spec -> normalize -> name (naming.Namer) -> filter -> transform
//	          -> resolve name collisions -> build cobra commands
//
// Each stage is pluggable through Options. Minimal use:
//
//	func main() {
//		forge.Main(
//			forge.WithName("petstore"),
//			forge.WithSpecData(specYAML),
//			forge.WithFilters(filter.ExcludeTags("internal")),
//		)
//	}
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/filter"
	"github.com/jleva12/forge-cli/naming"
	"github.com/jleva12/forge-cli/output"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
	"github.com/jleva12/forge-cli/transform"
	"github.com/jleva12/forge-cli/transport"
)

// App builds a CLI from an OpenAPI document.
type App struct {
	name, short, long, version string
	envPrefix                  string

	specLocation string
	specData     []byte
	loadOpts     spec.LoadOptions

	namer             naming.Namer
	filters           []filter.Filter
	transforms        []transform.Transform
	vendorExtensions  bool
	groupDescriptions map[string]string
	commandHooks      []CommandHook
	extraCommands     []*cobra.Command
	builtins          bool

	baseURL       string
	httpClient    *http.Client
	middleware    []transport.Middleware
	requestHooks  []RequestHook
	responseHooks []ResponseHook

	specAuth      bool
	authOverrides map[string]auth.Provider
	fallbackAuth  auth.Provider
	authCommands  []*cobra.Command
	credStore     auth.Store
	credKey       string

	profile           string
	extraTrustedHosts []string
	untrustedHint     string
	auth              *auth.Registry

	formatters    map[string]output.Formatter
	defaultOutput string

	api      *spec.API
	ops      []*spec.Operation // exposed
	excluded []*spec.Operation // removed by filters
}

// New creates an App. Nothing is loaded until Load, Command or Execute runs.
func New(opts ...Option) *App {
	a := &App{
		name:              "forge",
		namer:             naming.Default{},
		vendorExtensions:  true,
		builtins:          true,
		specAuth:          true,
		groupDescriptions: map[string]string{},
		authOverrides:     map[string]auth.Provider{},
		formatters:        output.Defaults(),
		defaultOutput:     "json",
	}
	for _, o := range opts {
		o(a)
	}
	if a.envPrefix == "" {
		a.envPrefix = naming.EnvName(a.name)
	}
	return a
}

// API returns the loaded API (nil before Load).
func (a *App) API() *spec.API { return a.api }

// Operations returns the operations exposed as commands (after Load).
func (a *App) Operations() []*spec.Operation { return a.ops }

// Excluded returns the operations removed by filters (after Load).
func (a *App) Excluded() []*spec.Operation { return a.excluded }

// Load runs the pipeline up to (but not including) building commands.
// It's idempotent.
func (a *App) Load(ctx context.Context) error {
	if a.ops != nil || a.excluded != nil {
		return nil
	}
	if a.api == nil {
		var err error
		switch {
		case a.specData != nil:
			a.api, err = spec.LoadData(ctx, a.specData, nil, a.loadOpts)
		case a.specLocation != "":
			a.loadOpts.HTTPClient = a.httpClient
			a.api, err = spec.Load(ctx, a.specLocation, a.loadOpts)
		default:
			err = errors.New("no OpenAPI spec configured (use WithSpec or WithSpecData)")
		}
		if err != nil {
			return err
		}
	}

	filters := a.filters
	transforms := a.transforms
	if a.vendorExtensions {
		filters = append([]filter.Filter{filter.ExcludeExtension(transform.ExtIgnore)}, filters...)
		transforms = append([]transform.Transform{transform.VendorExtensions()}, transforms...)
	}

	a.ops, a.excluded = []*spec.Operation{}, []*spec.Operation{}
outer:
	for _, op := range a.api.Operations {
		op.Group = a.namer.Group(op)
		op.Name = a.namer.Command(op)
		for _, p := range op.AllParams() {
			p.Flag = a.namer.Flag(op, p)
		}
		for _, f := range filters {
			if !f(op) {
				a.excluded = append(a.excluded, op)
				continue outer
			}
		}
		a.ops = append(a.ops, op)
	}
	for _, op := range a.ops {
		for _, t := range transforms {
			t(op)
		}
	}
	a.resolveCommandCollisions()
	for _, op := range a.ops {
		resolveFlagCollisions(op)
	}

	a.auth = auth.NewRegistry()
	if a.specAuth {
		for scheme, p := range auth.FromSpec(a.api, a.envPrefix) {
			a.auth.Set(scheme, p)
		}
	}
	for scheme, p := range a.authOverrides {
		a.auth.Set(scheme, p)
	}
	a.auth.Fallback = a.fallbackAuth
	a.installStoredAuth()
	return nil
}

// resolveCommandCollisions makes command names unique within each group and
// keeps root-level commands from shadowing groups or built-ins.
func (a *App) resolveCommandCollisions() {
	taken := map[string]map[string]bool{"": {"help": true, "completion": true}}
	for _, op := range a.ops {
		if op.Group != "" {
			taken[""][op.Group] = true
		}
	}
	for _, op := range a.ops {
		names := taken[op.Group]
		if names == nil {
			names = map[string]bool{}
			taken[op.Group] = names
		}
		base := op.Name
		if base == "" {
			base = naming.Default{}.Command(op)
		}
		name := base
		if names[name] {
			name = base + "-" + naming.Kebab(op.Method)
		}
		for i := 2; names[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		op.Name = name
		names[name] = true
	}
}

// Command builds the root cobra command.
func (a *App) Command(ctx context.Context) (*cobra.Command, error) {
	if err := a.Load(ctx); err != nil {
		return nil, err
	}
	return a.buildRoot(), nil
}

// Execute builds the CLI and runs it with os.Args.
func (a *App) Execute(ctx context.Context) error {
	root, err := a.Command(ctx)
	if err != nil {
		return err
	}
	return root.ExecuteContext(ctx)
}

// Main builds and runs a CLI, printing errors and exiting with a non-zero
// status on failure.
func Main(opts ...Option) {
	os.Exit(Run(context.Background(), New(opts...)))
}

// Run executes app and returns a process exit code.
func Run(ctx context.Context, app *App) int {
	err := app.Execute(ctx)
	if err == nil {
		return 0
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		fmt.Fprintln(os.Stderr, style.Stderr().ErrorLabel(), err)
	}
	return 1
}

// HTTPError is returned when the API responds with a status >= 400. The
// response body has already been printed.
type HTTPError struct {
	Op     *spec.Operation
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Op.Key(), e.Status, http.StatusText(e.Status))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

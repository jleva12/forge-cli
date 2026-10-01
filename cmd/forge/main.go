// Command forge is a generic CLI for any OpenAPI spec. Register APIs under
// names and switch between them:
//
//	forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
//	forge add billing ./billing.yaml --read-only
//	forge use petstore
//	forge commands
//	forge --api billing invoices list      # one-off, without switching
//	forge --spec ./other.yaml routes       # ad-hoc spec, not registered
//
// To ship a CLI for one specific API, embed its spec and call forge.Main
// instead (see examples/petstore).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"forge-cli/auth"
	"forge-cli/forge"
	"forge-cli/spec"
	"forge-cli/style"
)

const setupHelp = `No API is active yet. Register one or more APIs by name, then pick one:

  forge add <name> <file|url>     register an API (repeat for as many as you like)
  forge use <name>                make it the active API
  forge apis                      list registered APIs

Or skip the registry:

  forge --spec <file|url> ...     use a spec for a single command
  export FORGE_SPEC=<file|url>    use a spec for the current shell

For example:

  forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
  forge commands`

func main() {
	ctx := context.Background()
	args := os.Args[1:]

	root, err := buildRoot(ctx, args)
	if err != nil && completing(args) {
		// Tab completion still offers the registry commands when the spec
		// can't be loaded.
		root, err = setupRoot(), nil
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, style.Stderr().ErrorLabel(), err)
		os.Exit(1)
	}
	if err := root.ExecuteContext(ctx); err != nil {
		var httpErr *forge.HTTPError
		if !errors.As(err, &httpErr) {
			fmt.Fprintln(os.Stderr, style.Stderr().ErrorLabel(), err)
		}
		os.Exit(1)
	}
}

func buildRoot(ctx context.Context, args []string) (*cobra.Command, error) {
	// Registry and completion commands never load a spec, so they work even
	// when the active API's spec is missing or unreachable. Shells run
	// "forge completion <shell>" at startup, so it must also be fast.
	if first := firstArg(args); managementCommands[first] || first == "completion" {
		return setupRoot(), nil
	}
	r, err := resolve(args)
	if err != nil {
		return nil, err
	}
	if r.cfg == nil {
		return setupRoot(), nil
	}

	long := ""
	if r.name != "" {
		long = fmt.Sprintf("Active API: %s (from %s). Switch with \"forge use <name>\"; list with \"forge apis\".", r.name, r.source)
	}
	opts := append(r.cfg.options(r.name),
		forge.WithName("forge"),
		forge.WithDescription("", long),
		forge.WithTransforms(avoidReservedNames),
	)
	if r.name != "" {
		// The registered API is a profile: its own login, env prefix and
		// trusted hosts.
		opts = append(opts,
			forge.WithProfile(r.name),
			forge.WithCredentialStore(auth.DefaultStore(), credentialKey(r.name)),
			forge.WithUntrustedHostHint(fmt.Sprintf(
				"To allow it, add it to trusted_hosts for [apis.%s] in the registry (\"forge apis --path\"), then run \"forge auth login\" again", r.name)),
		)
	} else {
		opts = append(opts, forge.WithUntrustedHostHint("Register the API with \"forge add <name> <spec> --trust-host <host>\" to allow it"))
	}
	stop := func() {}
	if strings.HasPrefix(r.cfg.Spec, "http://") || strings.HasPrefix(r.cfg.Spec, "https://") {
		stop = style.Spinner(os.Stderr, "Loading "+r.cfg.Spec)
	}
	root, err := forge.New(opts...).Command(ctx)
	stop()
	if err != nil {
		hint := ""
		switch r.source {
		case "active API", "FORGE_API", "--api":
			hint = "\nFix the spec location in the registry file (\"forge apis --path\"), or switch APIs with \"forge use <name>\"."
		}
		label := r.cfg.Spec
		if r.name != "" {
			label = fmt.Sprintf("%s (%s)", r.name, r.cfg.Spec)
		}
		return nil, fmt.Errorf("loading %s: %w%s", label, err, hint)
	}
	// Registered so cobra accepts them; their values were read in resolve.
	root.PersistentFlags().String("spec", "", "OpenAPI spec file or URL for this command (overrides the active API)")
	root.PersistentFlags().String("api", "", "registered API to use for this command (overrides the active API)")
	root.RegisterFlagCompletionFunc("api", completeAPINames)
	root.AddGroup(&cobra.Group{ID: groupRegistry, Title: "API Registry Commands:"})
	root.AddCommand(registryCommands()...)
	return root, nil
}

// setupRoot is the command tree when no API is loaded: registry commands plus
// setup instructions.
func setupRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "forge",
		Short:         "Turn any OpenAPI spec into a CLI",
		Long:          "forge turns OpenAPI specs into CLIs.\n\n" + setupHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Accept anything so an API command run without an active API gets the
		// setup instructions rather than a flag parsing error.
		Args:               cobra.ArbitraryArgs,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return errors.New(setupHelp)
		},
	}
	root.PersistentFlags().String("spec", "", "OpenAPI spec file or URL for this command")
	root.PersistentFlags().String("api", "", "registered API to use for this command")
	root.RegisterFlagCompletionFunc("api", completeAPINames)
	forge.StyleRoot(root)
	root.AddGroup(&cobra.Group{ID: groupRegistry, Title: "API Registry Commands:"})
	root.AddCommand(registryCommands()...)
	forge.AddCompletionCommand(root)
	return root
}

type resolution struct {
	name   string // registered name, empty for an ad-hoc spec
	cfg    *APIConfig
	source string
}

// resolve picks the API for this invocation. Precedence: --spec, --api,
// FORGE_SPEC, FORGE_API, then the active API from "forge use".
func resolve(args []string) (resolution, error) {
	flagSpec, flagAPI := flagValue(args, "spec"), flagValue(args, "api")
	if flagSpec != "" {
		return resolution{cfg: &APIConfig{Spec: flagSpec}, source: "--spec"}, nil
	}
	reg, err := loadRegistry()
	if err != nil {
		return resolution{}, err
	}
	named := func(name, source string) (resolution, error) {
		cfg, err := reg.lookup(name)
		if err != nil {
			return resolution{}, fmt.Errorf("%s: %w", source, err)
		}
		return resolution{name: name, cfg: cfg, source: source}, nil
	}
	if flagAPI != "" {
		return named(flagAPI, "--api")
	}
	if v := os.Getenv("FORGE_SPEC"); v != "" {
		return resolution{cfg: &APIConfig{Spec: v}, source: "FORGE_SPEC"}, nil
	}
	if v := os.Getenv("FORGE_API"); v != "" {
		return named(v, "FORGE_API")
	}
	if reg.Active != "" {
		return named(reg.Active, "active API")
	}
	return resolution{}, nil
}

// credentialKey is where an API's login is stored.
func credentialKey(name string) string { return "api:" + name }

// avoidReservedNames keeps API commands from shadowing registry commands.
func avoidReservedNames(op *spec.Operation) {
	if managementCommands[op.Group] {
		op.Group += "-api"
	}
	if op.Group == "" && managementCommands[op.Name] {
		op.Name += "-api"
	}
}

// flagValue finds --name value or --name=value before cobra parses flags.
func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == "--" {
			break
		}
		if v, ok := strings.CutPrefix(arg, "--"+name+"="); ok {
			return v
		}
		if arg == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// completing reports whether args is a tab-completion request from the shell.
func completing(args []string) bool {
	return len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd)
}

// firstArg returns the first non-flag argument, skipping the values of the
// --spec and --api flags. For a completion request it's the first complete
// word: the last argument is the one being typed.
func firstArg(args []string) string {
	if completing(args) {
		args = args[1:max(1, len(args)-1)]
	}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--spec" || a == "--api":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

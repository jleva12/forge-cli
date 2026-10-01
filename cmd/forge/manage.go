package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/forge"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
)

// Registry management commands. They run without loading any spec, so they
// keep working when the active API's spec is unreachable.
var managementCommands = map[string]bool{"add": true, "use": true, "apis": true, "remove": true, "rm": true, "refresh": true}

const groupRegistry = "registry"

func registryCommands() []*cobra.Command {
	cmds := []*cobra.Command{addCommand(), useCommand(), apisCommand(), removeCommand(), refreshCommand()}
	for _, c := range cmds {
		c.GroupID = groupRegistry
	}
	return cmds
}

func addCommand() *cobra.Command {
	var (
		name, specLoc, file string
		serverVars          []string
		cfg                 APIConfig
		use, force, noCheck bool
	)
	cmd := &cobra.Command{
		Use:   "add [name] [spec]",
		Short: "Register an API under a unique name",
		Long: `Register an OpenAPI spec (file or URL) under a unique name. The spec is
checked before it's saved. Switch to it with "forge use <name>".

Filters and settings are stored with the API and can be edited later in the
registry file (see "forge apis --path").

Register many APIs at once from a TOML file with --file; it uses the same
format as the registry ("forge apis --toml" prints one to share).`,
		Example: `  forge add petstore https://petstore3.swagger.io/api/v3/openapi.json
  forge add --name billing --spec ./billing.yaml --server https://billing.internal --read-only
  forge add acme https://api.example.com/openapi.json --server-var tenant=acme
  forge add stripe ./stripe.json --include-tag customers --include-tag charges --use
  forge add --file team-apis.toml`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if file != "" {
				if len(args) > 0 || name != "" || specLoc != "" {
					return errors.New("--file can't be combined with a name or spec")
				}
				return addFromFile(cmd.Context(), cmd, reg, file, force, noCheck)
			}

			if len(args) > 0 {
				name = args[0]
			}
			if len(args) > 1 {
				specLoc = args[1]
			}
			if name == "" || specLoc == "" {
				return errors.New(`need a name and a spec: forge add <name> <file|url>`)
			}
			if err := checkName(name); err != nil {
				return err
			}
			if cfg.ServerVars, err = parseServerVars(serverVars); err != nil {
				return err
			}
			old, exists := reg.APIs[name]
			if exists && !force {
				return fmt.Errorf("an API named %q already exists (use --force to replace it, or pick another name)", name)
			}
			if cfg.Spec, err = absSpec(specLoc); err != nil {
				return err
			}
			if other := reg.envPrefixConflict(name, &cfg); other != "" {
				return fmt.Errorf("%q would share environment variables (%s_*) with %q, so they could pick up each other's credentials; choose another name or set --env-prefix",
					name, cfg.envPrefix(name), other)
			}
			p := style.For(out)
			if !noCheck {
				stop := style.Spinner(cmd.ErrOrStderr(), "Loading "+cfg.Spec)
				summary, err := inspect(cmd.Context(), name, &cfg)
				stop()
				if err != nil {
					return fmt.Errorf("not added: %w", err)
				}
				printSummary(out, fmt.Sprintf("Added %s: ", p.Bold(name)), summary)
			} else {
				fmt.Fprintln(out, p.Success(fmt.Sprintf("Added %s %s", p.Bold(name), p.Dim("(spec not checked)"))))
			}
			cfg.Added = time.Now().UTC().Truncate(time.Second)
			reg.APIs[name] = &cfg
			if exists && old.endpoint() != cfg.endpoint() {
				// A replaced API may point somewhere else: never carry its login over.
				if auth.DefaultStore().Delete(credentialKey(name)) == nil {
					fmt.Fprintln(out, p.Warning(fmt.Sprintf("Deleted the stored login for the previous %s; run %s again.",
						name, p.Command("forge --api "+name+" auth login"))))
				}
			}

			if use || reg.Active == "" {
				reg.Active = name
			}
			path, err := reg.save()
			if err != nil {
				return err
			}
			if reg.Active == name {
				fmt.Fprintln(out, p.Info(fmt.Sprintf("Active API is now %s. Run %s to see what you can do.", p.Bold(name), p.Command("forge commands"))))
			} else {
				fmt.Fprintln(out, p.Info(fmt.Sprintf("Switch to it with %s %s", p.Command("forge use "+name), p.Dim("(active: "+reg.Active+")"))))
			}
			fmt.Fprintln(out, p.Info(fmt.Sprintf("Log in with %s %s", p.Command("forge --api "+name+" auth login"),
				p.Dim("(or set "+cfg.envPrefix(name)+"_* environment variables)"))))
			fmt.Fprintln(out, p.Dim("  Saved to "+path))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "unique name for the API")
	f.StringVar(&specLoc, "spec", "", "OpenAPI spec file or URL")
	f.StringVar(&file, "file", "", "register every API in a TOML file")
	f.StringVar(&cfg.Server, "server", "", "base URL, overriding the spec's servers")
	f.StringArrayVar(&serverVars, "server-var", nil, "value for a variable in the spec's server URL, e.g. tenant=acme for https://{tenant}.example.com (repeatable)")
	f.StringVar(&cfg.EnvPrefix, "env-prefix", "", "environment variable prefix for credentials (default: the name, upper-cased)")
	f.StringArrayVar(&cfg.TrustedHosts, "trust-host", nil, "extra host allowed to receive credentials, e.g. staging.example.com (repeatable)")
	f.StringArrayVar(&cfg.IncludeGroups, "include-group", nil, "only expose this command group, e.g. customers (repeatable)")
	f.StringArrayVar(&cfg.ExcludeGroups, "exclude-group", nil, "hide this command group (repeatable)")
	f.StringArrayVar(&cfg.IncludeTags, "include-tag", nil, "only expose operations with this tag (repeatable)")
	f.StringArrayVar(&cfg.ExcludeTags, "exclude-tag", nil, "hide operations with this tag (repeatable)")
	f.StringArrayVar(&cfg.IncludePaths, "include-path", nil, "only expose paths matching this glob, e.g. /v1/** (repeatable)")
	f.StringArrayVar(&cfg.ExcludePaths, "exclude-path", nil, "hide paths matching this glob (repeatable)")
	f.StringArrayVar(&cfg.ExcludeOperations, "exclude-operation", nil, "hide this operationId (repeatable)")
	f.BoolVar(&cfg.ExcludeDeprecated, "exclude-deprecated", false, "hide deprecated operations")
	f.BoolVar(&cfg.ReadOnly, "read-only", false, "only expose GET/HEAD/OPTIONS operations")
	f.BoolVar(&use, "use", false, "make it the active API")
	f.BoolVar(&force, "force", false, "replace an existing API with the same name")
	f.BoolVar(&noCheck, "no-check", false, "don't load the spec before saving")
	return cmd
}

func addFromFile(ctx context.Context, cmd *cobra.Command, reg *Registry, file string, force, noCheck bool) error {
	incoming, err := readRegistryFile(file)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	p := style.For(out)
	skip := func(name, why string) { fmt.Fprintf(out, "  %s %s %s\n", p.Yellow("skip"), p.Bold(name), p.Dim(why)) }
	fail := func(name, why string) { fmt.Fprintf(out, "  %s %s %s\n", p.Red("fail"), p.Bold(name), why) }
	added, failed := 0, 0
	for _, name := range incoming.names() {
		cfg := incoming.APIs[name]
		if err := checkName(name); err != nil {
			fail(name, err.Error())
			failed++
			continue
		}
		old, exists := reg.APIs[name]
		if exists && !force {
			skip(name, "already registered (use --force to replace)")
			continue
		}
		if other := reg.envPrefixConflict(name, cfg); other != "" {
			fail(name, fmt.Sprintf("shares environment variables (%s_*) with %s; set env_prefix", cfg.envPrefix(name), other))
			failed++
			continue
		}
		if cfg.Spec == "" {
			fail(name, "missing spec")
			failed++
			continue
		}
		if !strings.HasPrefix(cfg.Spec, "http://") && !strings.HasPrefix(cfg.Spec, "https://") && !filepath.IsAbs(cfg.Spec) {
			// Relative paths in a shared file are relative to that file.
			cfg.Spec = filepath.Join(filepath.Dir(file), cfg.Spec)
		}
		if !noCheck {
			stop := style.Spinner(cmd.ErrOrStderr(), "Loading "+cfg.Spec)
			summary, err := inspect(ctx, name, cfg)
			stop()
			if err != nil {
				fail(name, err.Error())
				failed++
				continue
			}
			fmt.Fprintf(out, "  %s  %s %s\n", p.Green("add"), p.Bold(name), p.Dim(strings.SplitN(summary, "\n", 2)[0]))
		} else {
			fmt.Fprintf(out, "  %s  %s\n", p.Green("add"), p.Bold(name))
		}
		if cfg.Added.IsZero() {
			cfg.Added = time.Now().UTC().Truncate(time.Second)
		}
		reg.APIs[name] = cfg
		if exists && old.endpoint() != cfg.endpoint() {
			auth.DefaultStore().Delete(credentialKey(name))
		}
		added++
	}
	if reg.Active == "" {
		if _, ok := reg.APIs[incoming.Active]; ok {
			reg.Active = incoming.Active
		} else if len(reg.APIs) > 0 {
			reg.Active = reg.names()[0]
		}
	}
	path, err := reg.save()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, p.Success(fmt.Sprintf("Added %d API(s); active: %s", added, p.Bold(reg.Active))))
	fmt.Fprintln(out, p.Dim("  Saved to "+path))
	if failed > 0 {
		return fmt.Errorf("%d API(s) could not be added", failed)
	}
	return nil
}

// inspect loads the spec with the API's settings and summarizes it. It also
// fills in the title and description. A spec registered by URL is always
// downloaded, and the cached copy updated.
func inspect(ctx context.Context, name string, cfg *APIConfig) (string, error) {
	opts := append(cfg.options(name), forge.WithName("forge"))
	cache, err := specCache()
	if err != nil {
		return "", err
	}
	if cache != nil {
		cache.Refresh = true
		opts = append(opts, forge.WithSpecCache(cache))
	}
	app := forge.New(opts...)
	if err := app.Load(ctx); err != nil {
		return "", err
	}
	api := app.API()
	cfg.Title = strings.TrimSpace(api.Title + " " + api.Version)
	cfg.Description = firstSentence(api.Description)
	summary := fmt.Sprintf("%s, %d commands", cfg.Title, len(app.Operations()))
	if n := len(app.Excluded()); n > 0 {
		summary += fmt.Sprintf(" (%d hidden by filters)", n)
	}
	if len(app.Operations()) == 0 && len(app.Excluded()) > 0 {
		summary += "\nWarning: the filters hide every operation. Available groups: " + groupList(app.Excluded(), 20)
	}
	if cfg.Server == "" {
		// The server commands will call: the first that's absolute or has a gap.
		for _, s := range api.Servers {
			if len(s.Missing) > 0 {
				flags := make([]string, len(s.Missing))
				for i, v := range s.Missing {
					flags[i] = "--server-var " + v + "=<value>"
				}
				summary += fmt.Sprintf("\nWarning: the spec's server URL %s needs a value for {%s}. Add the API again with %s --force, or set a full URL with --server.",
					s.URL, strings.Join(s.Missing, "}, {"), strings.Join(flags, " "))
				break
			}
			if strings.HasPrefix(s.URL, "http://") || strings.HasPrefix(s.URL, "https://") {
				break
			}
		}
	}
	return summary, nil
}

// parseServerVars turns --server-var name=value pairs into a map.
func parseServerVars(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	vars := map[string]string{}
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		name = strings.TrimSuffix(strings.TrimPrefix(name, "{"), "}") // accept {tenant}=acme
		if !ok || name == "" {
			return nil, fmt.Errorf("--server-var %q: want name=value, e.g. tenant=acme", pair)
		}
		vars[name] = value
	}
	return vars, nil
}

// groupList summarizes the command groups of ops, e.g. for filter hints.
func groupList(ops []*spec.Operation, max int) string {
	seen := map[string]bool{}
	var groups []string
	for _, op := range ops {
		if op.Group != "" && !seen[op.Group] {
			seen[op.Group] = true
			groups = append(groups, op.Group)
		}
	}
	sort.Strings(groups)
	if len(groups) > max {
		return strings.Join(groups[:max], ", ") + fmt.Sprintf(", ... (%d more)", len(groups)-max)
	}
	return strings.Join(groups, ", ")
}

func useCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "use [name]",
		Short: "Switch the active API",
		Long: `Switch the active API. Every API command then targets it until you switch
again. Without a name, opens an interactive picker on a terminal (arrow keys,
type to filter, enter to select), or shows the active API otherwise.

To run a single command against another API without switching, use
--api <name>, or set FORGE_API for the current shell.`,
		Example:           "  forge use petstore\n  forge use --name billing\n  forge use            # pick from a list",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeAPINames,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				name = args[0]
			}
			if name == "" && canPick(cmd) && len(reg.APIs) > 0 {
				return pickAPI(cmd, reg)
			}
			if name == "" {
				r, err := resolve(os.Args[1:])
				if err != nil {
					return err
				}
				if r.cfg == nil {
					return errors.New(setupHelp)
				}
				label := r.name
				if label == "" {
					label = r.cfg.Spec
				}
				p := style.For(out)
				fmt.Fprintf(out, "%s %s %s\n", p.Dim("Active:"), p.Bold(label), p.Dim("(from "+r.source+")"))
				if r.name != "" {
					fmt.Fprintf(out, "%s %s\n%s %s\n", p.Dim("Spec:  "), r.cfg.Spec, p.Dim("Login: "), styledLogin(p, r.name))
				}
				return nil
			}
			if looksLikeSpec(name) {
				return fmt.Errorf("%q looks like a spec, not a name; register it first with \"forge add <name> %s\"", name, name)
			}
			return switchActive(out, reg, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name of the API to switch to")
	cmd.RegisterFlagCompletionFunc("name", completeAPINames)
	return cmd
}

// switchActive makes name the active API and reports the new profile.
func switchActive(out io.Writer, reg *Registry, name string) error {
	cfg, err := reg.lookup(name)
	if err != nil {
		return err
	}
	reg.Active = name
	if _, err := reg.save(); err != nil {
		return err
	}
	p := style.For(out)
	title := ""
	if cfg.Title != "" {
		title = " " + p.Dim("("+cfg.Title+")")
	}
	fmt.Fprintln(out, p.Success(fmt.Sprintf("Active API is now %s%s", p.Bold(name), title)))
	fmt.Fprintf(out, "  %s %s\n", p.Dim("Login:"), styledLogin(p, name))
	fmt.Fprintln(out, p.Dim("  All commands, including auth, now apply to this API. Run ")+p.Command("forge commands")+p.Dim(" to see what you can do."))
	for _, env := range []string{"FORGE_SPEC", "FORGE_API"} {
		if v := os.Getenv(env); v != "" {
			fmt.Fprintln(out, p.Warning(fmt.Sprintf("%s=%s is set in this shell and takes precedence.", env, v)))
		}
	}
	return nil
}

// canPick reports whether a person is at the keyboard: the command reads the
// real stdin and writes to the real stdout, and both are terminals. Scripts,
// pipes and agents get the plain, non-interactive output instead.
func canPick(cmd *cobra.Command) bool {
	return cmd.InOrStdin() == os.Stdin && cmd.OutOrStdout() == os.Stdout && style.Interactive()
}

// pickAPI shows an arrow-key menu of registered APIs and switches to the chosen one.
func pickAPI(cmd *cobra.Command, reg *Registry) error {
	names := reg.names()
	store := auth.DefaultStore()
	items := make([]style.SelectItem, len(names))
	initial := 0
	for i, name := range names {
		login := "no login"
		if cred, err := store.Get(credentialKey(name)); err == nil {
			login = string(cred.Type) + " login"
		}
		items[i] = style.SelectItem{Columns: []string{name, reg.APIs[name].Title, login}, Marked: name == reg.Active}
		if name == reg.Active {
			initial = i
		}
	}
	idx, err := style.Select(os.Stdin, os.Stdout, style.SelectOptions{Title: "Switch API", Items: items, Initial: initial})
	if errors.Is(err, style.ErrCanceled) {
		return nil
	}
	if err != nil {
		return err
	}
	return switchActive(cmd.OutOrStdout(), reg, names[idx])
}

func apisCommand() *cobra.Command {
	var asTOML, showPath, list bool
	cmd := &cobra.Command{
		Use:   "apis",
		Short: "List registered APIs and pick the active one",
		Long: `List registered APIs. On a terminal this is an interactive picker: use the
arrow keys (or type to filter) and press enter to make an API active, or esc
to leave it unchanged. When piped, or with --list, it prints a table.`,
		Example: "  forge apis                            # pick with the arrow keys\n  forge apis --list                     # print the table\n  forge apis --toml > team-apis.toml   # share; import with forge add --file\n  forge apis --path",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if showPath {
				path, err := registryPath()
				if err != nil {
					return err
				}
				fmt.Fprintln(out, path)
				return nil
			}
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			if !asTOML && !list && canPick(cmd) && len(reg.APIs) > 0 {
				return pickAPI(cmd, reg)
			}
			if asTOML {
				data, err := reg.encode()
				if err != nil {
					return err
				}
				_, err = out.Write(data)
				return err
			}
			p := style.For(out)
			if len(reg.APIs) == 0 {
				fmt.Fprintln(out, p.Info("No APIs registered. Add one with "+p.Command("forge add <name> <file|url>")))
				return nil
			}
			t := style.NewTable(out, " ", "NAME", "TITLE", "LOGIN", "FILTERS", "SPEC")
			store := auth.DefaultStore()
			for _, name := range reg.names() {
				c := reg.APIs[name]
				marker, label := "", name
				if name == reg.Active {
					marker, label = p.Green("●"), p.Green(p.Bold(name))
				}
				login := p.Dim("-")
				if cred, err := store.Get(credentialKey(name)); err == nil {
					login = p.Green(string(cred.Type))
				}
				filters := filterSummary(c)
				if filters == "-" {
					filters = p.Dim(filters)
				} else {
					filters = p.Yellow(filters)
				}
				t.Row(marker, label, c.Title, login, filters, p.Dim(c.Spec))
			}
			if err := t.Render(out); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%s\n", p.Dim("● = active. Switch with ")+p.Command("forge use <name>")+p.Dim("."))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asTOML, "toml", false, "print the registry as TOML (importable with forge add --file)")
	cmd.Flags().BoolVar(&showPath, "path", false, "print the registry file location")
	cmd.Flags().BoolVarP(&list, "list", "l", false, "print the table instead of the interactive picker")
	return cmd
}

func removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:               "remove <name>...",
		Aliases:           []string{"rm"},
		Short:             "Unregister APIs",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeAPINames,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			for _, name := range args {
				if _, err := reg.lookup(name); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			for _, name := range args {
				p := style.For(out)
				delete(reg.APIs, name)
				fmt.Fprintln(out, p.Success("Removed "+p.Bold(name)))
				if err := auth.DefaultStore().Delete(credentialKey(name)); err == nil {
					fmt.Fprintln(out, p.Dim("  Deleted its stored credentials."))
				}
				if reg.Active == name {
					reg.Active = ""
					fmt.Fprintln(out, p.Warning("It was the active API; pick another with "+p.Command("forge use <name>")))
				}
			}
			_, err = reg.save()
			return err
		},
	}
}

func refreshCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh [name]...",
		Short: "Download the latest specs",
		Long: `Download the latest spec of each named API, or of every registered API, and
update their titles in "forge apis".

forge keeps a copy of each spec registered by URL, so it doesn't download the
spec on every run. It checks the server for a newer version once the copy is a
day old (set FORGE_SPEC_MAX_AGE to change that, e.g. 1h or 0). Refresh checks
now.`,
		Example: `  forge refresh            # every registered API
  forge refresh petstore`,
		ValidArgsFunction: completeAPINames,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				if names = reg.names(); len(names) == 0 {
					return errors.New(`no APIs are registered yet (add one with "forge add <name> <spec>")`)
				}
			}
			for _, name := range names {
				if _, err := reg.lookup(name); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			p := style.For(out)
			failed := 0
			for _, name := range names {
				cfg := reg.APIs[name]
				stop := style.Spinner(cmd.ErrOrStderr(), "Loading "+cfg.Spec)
				summary, err := inspect(cmd.Context(), name, cfg)
				stop()
				if err != nil {
					fmt.Fprintf(out, "  %s %s %s\n", p.Red("fail"), p.Bold(name), err)
					failed++
					continue
				}
				fmt.Fprintf(out, "  %s   %s %s\n", p.Green("ok"), p.Bold(name), p.Dim(strings.SplitN(summary, "\n", 2)[0]))
			}
			if _, err := reg.save(); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d API(s) could not be refreshed", failed)
			}
			return nil
		},
	}
}

// printSummary prints an inspect summary, styling its warning lines.
func printSummary(out io.Writer, prefix, summary string) {
	p := style.For(out)
	parts := strings.Split(summary, "\nWarning: ")
	fmt.Fprintln(out, p.Success(prefix+parts[0]))
	for _, warning := range parts[1:] {
		fmt.Fprintln(out, p.Warning(warning))
	}
}

// styledLogin is loginSummary with color.
func styledLogin(p style.Painter, name string) string {
	s := loginSummary(name)
	if strings.HasPrefix(s, "none") {
		return p.Yellow(s)
	}
	return p.Green(s)
}

// loginSummary describes the stored login for an API without revealing secrets.
func loginSummary(name string) string {
	cred, err := auth.DefaultStore().Get(credentialKey(name))
	if err != nil {
		return fmt.Sprintf("none (run \"forge auth login\" while %s is active)", name)
	}
	return cred.Summary()
}

func completeAPINames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	reg, err := loadRegistry()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range reg.names() {
		out = append(out, n+"\t"+reg.APIs[n].Title)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func filterSummary(c *APIConfig) string {
	var parts []string
	add := func(label string, vals []string) {
		if len(vals) > 0 {
			parts = append(parts, label+"="+strings.Join(vals, ","))
		}
	}
	add("groups", c.IncludeGroups)
	add("-groups", c.ExcludeGroups)
	add("tags", c.IncludeTags)
	add("-tags", c.ExcludeTags)
	add("paths", c.IncludePaths)
	add("-paths", c.ExcludePaths)
	add("-ops", c.ExcludeOperations)
	if c.ExcludeDeprecated {
		parts = append(parts, "-deprecated")
	}
	if c.ReadOnly {
		parts = append(parts, "read-only")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func absSpec(location string) (string, error) {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return location, nil
	}
	// Store absolute paths so forge works from any directory.
	return filepath.Abs(location)
}

func looksLikeSpec(s string) bool {
	return strings.Contains(s, "/") || strings.HasPrefix(s, "http") ||
		strings.HasSuffix(s, ".json") || strings.HasSuffix(s, ".yaml") || strings.HasSuffix(s, ".yml")
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i+1]
	}
	return s
}

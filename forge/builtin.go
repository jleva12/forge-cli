package forge

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/catalog"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
)

// routesCommand lists the operations exposed as commands, which makes it
// easy to check the effect of filters and transforms.
func (a *App) routesCommand() *cobra.Command {
	var excluded bool
	cmd := &cobra.Command{
		Use:   "routes",
		Short: "List API routes and the commands they map to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			p := style.For(out)
			if excluded {
				t := style.NewTable(out, "METHOD", "PATH", "OPERATION ID", "TAGS")
				for _, op := range a.excluded {
					t.Row(p.Method(op.Method), op.Path, p.Dim(op.ID), p.Dim(strings.Join(op.Tags, ",")))
				}
				return t.Render(out)
			}
			t := style.NewTable(out, "METHOD", "PATH", "COMMAND", "NOTES")
			for _, op := range a.ops {
				t.Row(p.Method(op.Method), op.Path, p.Command(a.name+" "+op.CommandPath()), p.Dim(routeNotes(op)))
			}
			if err := t.Render(out); err != nil {
				return err
			}
			if n := len(a.excluded); n > 0 {
				errw := cmd.ErrOrStderr()
				fmt.Fprintf(errw, "\n%s\n", style.For(errw).Dim(fmt.Sprintf("%d route(s) excluded by filters; see --excluded", n)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&excluded, "excluded", false, "list the routes removed by filters instead")
	return cmd
}

// commandsCommand prints every command with a full invocation template, so a
// person or agent can pick one and run it without reading each --help.
func (a *App) commandsCommand() *cobra.Command {
	var (
		requiredOnly bool
		markdown     bool
	)
	cmd := &cobra.Command{
		Use:   "commands [group]",
		Short: "List all API commands with the arguments to run them",
		Long: `List every API command with a description and a complete invocation.

Required arguments are shown bare and optional ones in [brackets].
<a|b|c> lists allowed values, <type>,... takes a comma-separated list,
<json> takes a JSON value and -d sends a request body (inline, @file, or - for stdin).
Run "` + a.name + ` <command> --help" for details on any of them.`,
		Example: fmt.Sprintf("  %[1]s commands\n  %[1]s commands pet --required-only\n  %[1]s commands --markdown", a.name),
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			type row struct{ desc, usage string }
			var rows []row
			for _, op := range a.ops {
				if op.Hidden || (len(args) == 1 && op.Group != args[0]) {
					continue
				}
				desc := op.Summary
				if desc == "" {
					desc = op.Key()
				}
				if op.Deprecated {
					desc = "[deprecated] " + desc
				}
				usage := catalog.Synopsis(op, a.name+" "+op.CommandPath(), catalog.SynopsisOptions{RequiredOnly: requiredOnly})
				rows = append(rows, row{desc, usage})
			}
			if len(rows) == 0 {
				if len(args) == 1 {
					return fmt.Errorf("no commands in group %q; run \"%s commands\" to list all", args[0], a.name)
				}
				return fmt.Errorf("no commands are exposed; filters hide all %d operations (see \"%s routes --excluded\")", len(a.excluded), a.name)
			}
			out := cmd.OutOrStdout()
			if markdown {
				fmt.Fprintln(out, "| Description | Command |\n|---|---|")
				for _, r := range rows {
					fmt.Fprintf(out, "| %s | `%s` |\n", strings.ReplaceAll(r.desc, "|", `\|`), strings.ReplaceAll(r.usage, "|", `\|`))
				}
				return nil
			}
			p := style.For(out)
			t := style.NewTable(out, "DESCRIPTION", "COMMAND")
			for _, r := range rows {
				desc := r.desc
				if strings.HasPrefix(desc, "[deprecated] ") {
					desc = p.Yellow("[deprecated]") + " " + p.Dim(strings.TrimPrefix(desc, "[deprecated] "))
				}
				t.Row(desc, styleSynopsis(p, r.usage))
			}
			return t.Render(out)
		},
	}
	cmd.Flags().BoolVar(&requiredOnly, "required-only", false, "show only required arguments")
	cmd.Flags().BoolVar(&markdown, "markdown", false, "print a Markdown table")
	return cmd
}

func routeNotes(op *spec.Operation) string {
	var notes []string
	if op.Hidden {
		notes = append(notes, "hidden")
	}
	if op.Deprecated {
		notes = append(notes, "deprecated")
	}
	if len(op.Aliases) > 0 {
		notes = append(notes, "aliases: "+strings.Join(op.Aliases, ","))
	}
	return strings.Join(notes, "; ")
}

// authCommand is the parent for auth utilities. Subcommands such as
// "login"/"logout" can be attached with WithAuthCommands.
func (a *App) authCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage and inspect authentication"}
	cmd.AddCommand(a.loginCommands()...)
	cmd.AddCommand(a.authCommands...)
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show security schemes and whether credentials are configured",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(a.api.SecuritySchemes) == 0 && a.auth.Fallback == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "The API declares no security schemes.")
				return nil
			}
			out := cmd.OutOrStdout()
			p := style.For(out)
			fmt.Fprintf(out, "%s %s\n%s %s\n\n", p.Dim("Profile:      "), p.Bold(a.profileLabel()),
				p.Dim("Trusted hosts:"), p.Cyan(strings.Join(a.TrustedHosts(), ", ")))
			t := style.NewTable(out, "SCHEME", "TYPE", "CONFIGURED", "SOURCE")
			for _, st := range a.authStatuses(cmd) {
				t.Row(p.Bold(st.Scheme), st.Type, p.YesNo(st.Configured), p.Dim(st.Source))
			}
			if a.auth.Fallback != nil {
				t.Row(p.Dim("(fallback)"), "-", p.YesNo(auth.Configured(cmd.Context(), a.auth.Fallback, a.authTarget(cmd))), p.Dim(describe(a.auth.Fallback)))
			}
			return t.Render(out)
		},
	})
	return cmd
}

func describe(p auth.Provider) string {
	switch d := p.(type) {
	case nil:
		return "no provider (unsupported scheme type)"
	case auth.Describer:
		return d.Describe()
	}
	return "custom provider"
}

var (
	synopsisOptional = regexp.MustCompile(`\[[^\]]*\]`)
	synopsisFlag     = regexp.MustCompile(`(^|\s)(--?[\w.-]+)`)
	synopsisValue    = regexp.MustCompile(`<[^>]*>(,\.\.\.)?`)
)

// styleSynopsis colors a command synopsis: the command in bold, required
// flags and values highlighted, optional arguments dimmed.
func styleSynopsis(p style.Painter, s string) string {
	if !p.Enabled() {
		return s
	}
	// The command path is everything before the first argument.
	cmdEnd := len(s)
	for _, marker := range []string{" <", " -", " ["} {
		if i := strings.Index(s, marker); i >= 0 && i < cmdEnd {
			cmdEnd = i
		}
	}
	head, rest := s[:cmdEnd], s[cmdEnd:]
	// Split out optional groups so they can be dimmed as a whole.
	var b strings.Builder
	last := 0
	for _, loc := range synopsisOptional.FindAllStringIndex(rest, -1) {
		b.WriteString(styleRequired(p, rest[last:loc[0]]))
		b.WriteString(p.Gray(rest[loc[0]:loc[1]]))
		last = loc[1]
	}
	b.WriteString(styleRequired(p, rest[last:]))
	return p.Command(head) + b.String()
}

func styleRequired(p style.Painter, s string) string {
	s = synopsisValue.ReplaceAllStringFunc(s, p.Value)
	return synopsisFlag.ReplaceAllString(s, "${1}"+p.Flag("${2}"))
}

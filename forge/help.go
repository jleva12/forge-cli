package forge

import (
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/style"
)

const flagNoColor = "no-color"

// usageTemplate is cobra's default usage template with styled headings,
// command names and flags. Styling is skipped automatically when stdout isn't
// a terminal.
const usageTemplate = `{{heading "Usage:"}}{{if .Runnable}}
  {{useLine .UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{cmdName .CommandPath}} {{dim "[command]"}}{{end}}{{if gt (len .Aliases) 0}}

{{heading "Aliases:"}}
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

{{heading "Examples:"}}
{{examples .Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

{{heading "Available Commands:"}}{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{cmdName (rpad .Name .NamePadding)}} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{heading .Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{cmdName (rpad .Name .NamePadding)}} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

{{heading "Additional Commands:"}}{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{cmdName (rpad .Name .NamePadding)}} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

{{heading "Flags:"}}
{{flags (.LocalFlags.FlagUsages | trimTrailingWhitespaces)}}{{end}}{{if .HasAvailableInheritedFlags}}

{{heading "Global Flags:"}}
{{flags (.InheritedFlags.FlagUsages | trimTrailingWhitespaces)}}{{end}}{{if .HasHelpSubCommands}}

{{heading "Additional help topics:"}}{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

{{dim "Use"}} {{cmdName (print .CommandPath " [command] --help")}} {{dim "for more information about a command."}}{{end}}
`

// helpTemplate styles the long description: the "  METHOD /path" line that
// operation commands end with gets method colors.
const helpTemplate = `{{with (or .Long .Short)}}{{longText . | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`

var (
	routeLine   = regexp.MustCompile(`(?m)^  (GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE|QUERY) (/\S*)$`)
	commentLine = regexp.MustCompile(`(?m)(\s+)(#.*)$`)
)

func init() {
	p := func() style.Painter { return style.Stdout() }
	cobra.AddTemplateFuncs(map[string]any{
		"heading": func(s string) string { return p().Heading(s) },
		"cmdName": func(s string) string { return p().Command(s) },
		"dim":     func(s string) string { return p().Dim(s) },
		"flags":   func(s string) string { return p().FlagUsages(s) },
		"useLine": func(s string) string {
			// "forge pet get <pet-id> [flags]": bold command, dim placeholders.
			parts := strings.Fields(s)
			for i, part := range parts {
				if strings.HasPrefix(part, "[") || strings.HasPrefix(part, "<") {
					parts[i] = p().Dim(part)
				} else {
					parts[i] = p().Command(part)
				}
			}
			return strings.Join(parts, " ")
		},
		"examples": func(s string) string {
			pp := p()
			return commentLine.ReplaceAllStringFunc(s, func(m string) string {
				sub := commentLine.FindStringSubmatch(m)
				return sub[1] + pp.Gray(sub[2])
			})
		},
		"longText": func(s string) string {
			pp := p()
			return routeLine.ReplaceAllStringFunc(s, func(m string) string {
				sub := routeLine.FindStringSubmatch(m)
				return "  " + pp.Method(sub[1]) + " " + pp.Bold(sub[2])
			})
		},
	})
}

// applyHelpStyle installs the styled templates and the --no-color flag.
func applyHelpStyle(root *cobra.Command) {
	root.SetUsageTemplate(usageTemplate)
	root.SetHelpTemplate(helpTemplate)
	root.PersistentFlags().Bool(flagNoColor, false, "disable colored output [env: NO_COLOR]")
}

// StyleRoot gives a root command that isn't built by an App (e.g. a setup
// screen) the same help styling and --no-color flag.
func StyleRoot(root *cobra.Command) { applyHelpStyle(root) }

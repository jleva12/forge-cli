package forge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/catalog"
	"github.com/jleva12/forge-cli/request"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
)

// Tools returns the exposed operations as agent tool definitions.
func (a *App) Tools(opts catalog.Options) []catalog.Tool {
	if opts.CLIName == "" {
		opts.CLIName = a.name
	}
	return catalog.Build(a.ops, opts)
}

// Catalog is the document printed by "tools": everything an agent needs to
// discover and call the API through this CLI.
type Catalog struct {
	CLI       string         `json:"cli"`
	API       CatalogAPI     `json:"api"`
	HowToCall string         `json:"how_to_call"`
	Auth      []AuthStatus   `json:"auth,omitempty"`
	Tools     []catalog.Tool `json:"tools"`
}

type CatalogAPI struct {
	Title       string `json:"title,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
}

// AuthStatus reports whether credentials for a security scheme are available.
type AuthStatus struct {
	Scheme     string `json:"scheme"`
	Type       string `json:"type"`
	Configured bool   `json:"configured"`
	Source     string `json:"source"`
}

func (a *App) authStatuses(cmd *cobra.Command) []AuthStatus {
	var out []AuthStatus
	target := a.authTarget(cmd)
	for _, name := range sortedKeys(a.api.SecuritySchemes) {
		s := a.api.SecuritySchemes[name]
		typ := s.Type
		if s.Scheme != "" {
			typ += "/" + s.Scheme
		}
		p := a.auth.Get(name)
		out = append(out, AuthStatus{
			Scheme: name, Type: typ, Source: describe(p),
			Configured: p != nil && auth.Configured(cmd.Context(), p, target),
		})
	}
	return out
}

// authTarget is the URL that credential checks pretend to call: the server
// requests would go to, since stored logins only apply to their own hosts.
func (a *App) authTarget(cmd *cobra.Command) string {
	if u, err := a.resolveBaseURL(cmd.Flags()); err == nil {
		return u
	}
	if hosts := a.TrustedHosts(); len(hosts) > 0 {
		return "https://" + hosts[0] + "/"
	}
	return "http://localhost/"
}

func (a *App) toolsCommand() *cobra.Command {
	var (
		format        string
		depth         int
		responseDepth int
		responses     bool
	)
	cmd := &cobra.Command{
		Use:   "tools [command...]",
		Short: "Describe commands as JSON tool definitions for AI agents",
		Long: `Describe the exposed API operations as machine-readable tool definitions.

Each tool has a name, a description, a JSON Schema for its input and a summary
of its responses. Input properties are the command's flag names, and a tool
can be run with:

  ` + a.name + ` call <tool> '<json input>'

Formats:
  catalog    full document with API info, auth status and tools (default)
  anthropic  array for the Claude Messages API "tools" parameter
  openai     array for the OpenAI Chat Completions "tools" parameter
  list       compact table of tool names and summaries`,
		Example: fmt.Sprintf("  %[1]s tools --format list\n  %[1]s tools pet\n  %[1]s tools --format anthropic > tools.json", a.name),
		RunE: func(cmd *cobra.Command, args []string) error {
			tools := a.Tools(catalog.Options{MaxSchemaDepth: depth})
			if len(args) > 0 {
				tools = matchTools(tools, strings.Join(args, " "))
				if len(tools) == 0 {
					return fmt.Errorf("no tools match %q; run \"%s tools --format list\"", strings.Join(args, " "), a.name)
				}
			}
			// Response schemas can dwarf everything else, so by default they're
			// only included when looking at a handful of tools.
			if !cmd.Flags().Changed("response-schemas") {
				responses = len(tools) <= 10
			}
			if responses && format == "catalog" {
				catalog.AddResponseSchemas(tools, responseDepth)
			}
			var v any
			switch format {
			case "catalog":
				v = Catalog{
					CLI: a.name,
					API: CatalogAPI{Title: a.api.Title, Version: a.api.Version, Description: strings.TrimSpace(a.api.Description)},
					HowToCall: fmt.Sprintf("Discover tools with `%[1]s tools --format list`, then inspect one with `%[1]s tools <tool name>` "+
						"(this includes its response schema). Run it with `%[1]s call <tool name> '<json input>'` where the input matches "+
						"the tool's input_schema (or pass - to read the JSON from stdin); equivalently, run the tool's command with "+
						"--<property> flags. Add --dry-run to print the HTTP request as curl without sending it. "+
						"Exit status is 0 on success and 1 on failure; API error bodies are written to stderr.", a.name),
					Auth:  a.authStatuses(cmd),
					Tools: tools,
				}
			case "anthropic":
				v = catalog.AnthropicTools(tools)
			case "openai":
				v = catalog.OpenAITools(tools)
			case "list":
				out := cmd.OutOrStdout()
				p := style.For(out)
				tbl := style.NewTable(out, "TOOL", "METHOD", "PATH", "SUMMARY")
				for _, t := range tools {
					tbl.Row(p.Command(t.Name), p.Method(t.Method), t.Path, p.Dim(t.Summary))
				}
				return tbl.Render(out)
			default:
				return fmt.Errorf("unknown format %q (catalog, anthropic, openai, list)", format)
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			enc.SetEscapeHTML(false)
			return enc.Encode(v)
		},
	}
	cmd.Flags().StringVar(&format, "format", "catalog", "catalog|anthropic|openai|list")
	cmd.Flags().IntVar(&depth, "max-depth", 4, "how deeply nested schemas are expanded")
	cmd.Flags().BoolVar(&responses, "response-schemas", false, "include success response schemas (default: only when 10 or fewer tools are selected)")
	cmd.Flags().IntVar(&responseDepth, "response-depth", 2, "how deeply response schemas are expanded")
	cmd.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"catalog", "anthropic", "openai", "list"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// matchTools selects tools by name ("pet_list") or command prefix ("pet", "pet list").
func matchTools(tools []catalog.Tool, query string) []catalog.Tool {
	q := catalog.NormalizeName(query)
	var out []catalog.Tool
	for _, t := range tools {
		if t.Name == q || catalog.NormalizeName(t.Op.Group) == q {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) callCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "call <tool> [input]",
		Short: "Run a tool by name with JSON input (for AI agents)",
		Long: `Run an API operation by tool name, with input as a JSON object matching the
tool's input_schema (see "` + a.name + ` tools"). The input can be given inline,
as @file, or as - to read from stdin. Global flags such as --dry-run,
--output and --server apply as usual.`,
		Example: fmt.Sprintf("  %[1]s call pet_list '{\"limit\": 5}'\n  echo '{\"name\": \"rex\"}' | %[1]s call pet_create -", a.name),
		Args:    cobra.RangeArgs(1, 2),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			var names []string
			for _, t := range a.Tools(catalog.Options{}) {
				names = append(names, t.Name+"\t"+t.Summary)
			}
			return names, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			tool, err := a.findTool(args[0])
			if err != nil {
				return err
			}
			raw := "{}"
			if len(args) == 2 {
				raw = args[1]
			}
			data, err := readBodyArg(raw, cmd.InOrStdin())
			if err != nil {
				return err
			}
			in, err := inputFromJSON(tool, data)
			if err != nil {
				return fmt.Errorf("%w\nRun \"%s tools %s\" to see the input schema", err, a.name, tool.Name)
			}
			return a.execute(cmd, tool.Op, in)
		},
	}
}

func (a *App) findTool(name string) (catalog.Tool, error) {
	tools := a.Tools(catalog.Options{IncludeHidden: true})
	q := catalog.NormalizeName(name)
	for _, t := range tools {
		if t.Name == q || catalog.NormalizeName(t.Op.CommandPath()) == q {
			return t, nil
		}
	}
	var close []string
	for _, t := range tools {
		if strings.Contains(t.Name, q) || levenshtein(t.Name, q) <= 3 {
			close = append(close, t.Name)
		}
	}
	msg := fmt.Sprintf("unknown tool %q", name)
	if len(close) > 0 {
		msg += "; did you mean: " + strings.Join(close, ", ")
	}
	return catalog.Tool{}, fmt.Errorf("%s (run \"%s tools --format list\")", msg, a.name)
}

// inputFromJSON maps a JSON object keyed by flag names onto request values.
func inputFromJSON(tool catalog.Tool, data []byte) (request.Input, error) {
	op := tool.Op
	in := request.Input{Values: map[*spec.Param]any{}}

	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return in, fmt.Errorf("input must be a JSON object: %w", err)
	}

	byFlag := map[string]*spec.Param{}
	for _, p := range op.AllParams() {
		byFlag[p.Flag] = p
	}
	var unknown []string
	for key, v := range obj {
		if key == catalog.BodyProperty && op.Body != nil && byFlag[key] == nil {
			if s, ok := v.(string); ok {
				in.RawBody = []byte(s)
			} else if b, err := json.Marshal(v); err == nil {
				in.RawBody = b
			}
			continue
		}
		p := byFlag[key]
		if p == nil {
			unknown = append(unknown, key)
			continue
		}
		if v == nil {
			continue
		}
		val, err := valueFromJSON(p, v)
		if err != nil {
			return in, err
		}
		in.Values[p] = val
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return in, fmt.Errorf("unknown input propert%s: %s", plural(len(unknown), "y", "ies"), strings.Join(unknown, ", "))
	}

	var missing []string
	for _, p := range op.AllParams() {
		if _, ok := in.Values[p]; ok {
			continue
		}
		v, ok, err := fallbackValue(p)
		if err != nil {
			return in, err
		}
		if ok {
			in.Values[p] = v
		} else if p.Required && p.In != spec.InBody {
			missing = append(missing, p.Flag)
		}
	}
	if len(missing) > 0 {
		return in, fmt.Errorf("missing required input propert%s: %s", plural(len(missing), "y", "ies"), strings.Join(missing, ", "))
	}
	return in, nil
}

func valueFromJSON(p *spec.Param, v any) (any, error) {
	switch p.Type {
	case spec.TypeArray:
		list, ok := v.([]any)
		if !ok {
			if s, isStr := v.(string); isStr {
				return parseArray(p, strings.Split(s, ","))
			}
			list = []any{v}
		}
		out := make([]any, len(list))
		for i, e := range list {
			x, err := scalarFromJSON(p, p.ItemType, e)
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	case spec.TypeObject:
		if p.In == spec.InBody {
			if s, ok := v.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(s), &decoded) == nil {
					return decoded, nil
				}
			}
			return v, nil
		}
		if s, ok := v.(string); ok {
			return s, nil
		}
		b, err := json.Marshal(v)
		return string(b), err
	}
	return scalarFromJSON(p, p.Type, v)
}

func scalarFromJSON(p *spec.Param, t spec.Type, v any) (any, error) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		s = x.String()
	case bool:
		s = strconv.FormatBool(x)
	default:
		return nil, fmt.Errorf("%s: expected %s, got %T", p.Flag, t, v)
	}
	val, err := parseScalar(t, s)
	if err != nil {
		return nil, fmt.Errorf("%s: expected %s, got %q", p.Flag, t, s)
	}
	if err := checkEnum(p, s); err != nil {
		return nil, err
	}
	return val, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

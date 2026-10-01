package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/jleva12/forge-cli/naming"
	"github.com/jleva12/forge-cli/output"
	"github.com/jleva12/forge-cli/request"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/style"
	"github.com/jleva12/forge-cli/transport"
)

// Global flag names. Operation flags that collide with these get a location prefix.
const (
	flagServer      = "server"
	flagOutput      = "output"
	flagHeader      = "header"
	flagDryRun      = "dry-run"
	flagVerbose     = "verbose"
	flagInclude     = "include"
	flagTimeout     = "timeout"
	flagBody        = "body"
	flagContentType = "content-type"
)

var reservedFlags = map[string]bool{
	flagServer: true, flagOutput: true, flagHeader: true, flagDryRun: true, flagVerbose: true,
	flagInclude: true, flagTimeout: true, flagBody: true, flagContentType: true, "help": true, "version": true,
	flagNoColor: true,
}

var reservedShorthands = map[string]bool{"o": true, "H": true, "v": true, "i": true, "d": true, "h": true}

const groupAPI, groupUtil = "api", "util"

func (a *App) buildRoot() *cobra.Command {
	short := a.short
	if short == "" && a.api.Title != "" {
		short = "CLI for " + a.api.Title
	}
	root := &cobra.Command{
		Use:           a.name,
		Short:         short,
		Long:          a.long,
		Version:       a.version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddGroup(&cobra.Group{ID: groupAPI, Title: "API Commands:"}, &cobra.Group{ID: groupUtil, Title: "Utility Commands:"})
	applyHelpStyle(root)

	pf := root.PersistentFlags()
	pf.String(flagServer, "", fmt.Sprintf("API base URL [env: %s_SERVER]", a.envPrefix))
	pf.StringP(flagOutput, "o", a.defaultOutput, "output format: "+strings.Join(sortedKeys(a.formatters), "|"))
	pf.StringArrayP(flagHeader, "H", nil, `extra request header, "Key: Value" (repeatable)`)
	pf.Bool(flagDryRun, false, "print the request as a curl command instead of sending it")
	pf.BoolP(flagVerbose, "v", false, "log request and response details to stderr")
	pf.BoolP(flagInclude, "i", false, "print response status and headers to stderr")
	pf.Duration(flagTimeout, 60*time.Second, "request timeout")
	root.RegisterFlagCompletionFunc(flagOutput, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return sortedKeys(a.formatters), cobra.ShellCompDirectiveNoFileComp
	})

	groups := map[string]*cobra.Command{}
	for _, op := range a.ops {
		parent := root
		if op.Group != "" {
			g, ok := groups[op.Group]
			if !ok {
				g = &cobra.Command{
					Use: op.Group, Short: a.groupDescription(op.Group), GroupID: groupAPI,
					// Runnable so unknown subcommands are an error rather than silently printing help.
					Args: cobra.NoArgs,
					RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
				}
				groups[op.Group] = g
				root.AddCommand(g)
			}
			parent = g
		}
		cmd := a.operationCommand(op)
		if parent == root {
			cmd.GroupID = groupAPI
		}
		parent.AddCommand(cmd)
	}
	// A group is hidden only if everything in it is hidden.
	for _, g := range groups {
		g.Hidden = true
		for _, c := range g.Commands() {
			g.Hidden = g.Hidden && c.Hidden
		}
	}

	var util []*cobra.Command
	if a.builtins {
		util = append(util, a.commandsCommand(), a.routesCommand(), a.authCommand(), a.toolsCommand(), a.callCommand())
	}
	for _, c := range append(util, a.extraCommands...) {
		for existing, _, _ := root.Find([]string{c.Name()}); existing != root; existing, _, _ = root.Find([]string{c.Name()}) {
			c.Use = "cli-" + c.Use
		}
		if c.GroupID == "" {
			c.GroupID = groupUtil
		}
		root.AddCommand(c)
	}
	root.SetHelpCommandGroupID(groupUtil)
	root.SetCompletionCommandGroupID(groupUtil)
	AddCompletionCommand(root)
	return root
}

func (a *App) groupDescription(group string) string {
	if d, ok := a.groupDescriptions[group]; ok {
		return d
	}
	for tag, desc := range a.api.Tags {
		if naming.Kebab(tag) == group && desc != "" {
			return firstSentence(desc)
		}
	}
	return "Operations on " + group
}

// --- Operation commands ------------------------------------------------------

func (a *App) operationCommand(op *spec.Operation) *cobra.Command {
	var positional []*spec.Param
	use := op.Name
	for _, p := range op.Params {
		if p.Positional {
			positional = append(positional, p)
			use += " <" + p.Flag + ">"
		}
	}

	long := op.Summary
	if op.Description != "" && op.Description != op.Summary {
		long = strings.TrimSpace(op.Summary + "\n\n" + op.Description)
	}
	long = strings.TrimSpace(long + "\n\n  " + op.Key())

	short := op.Summary
	if short == "" {
		short = op.Key()
	}

	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Long:    long,
		Aliases: op.Aliases,
		Hidden:  op.Hidden,
		Example: op.Example,
		Args:    positionalArgs(positional),
	}
	if op.Deprecated {
		cmd.Short = "[deprecated] " + cmd.Short
	}
	if len(positional) > 0 {
		cmd.ValidArgsFunction = func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) < len(positional) {
				return positional[len(args)].Enum, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}

	fs := cmd.Flags()
	usedShort := map[string]bool{}
	for k := range reservedShorthands {
		usedShort[k] = true
	}
	for _, p := range op.AllParams() {
		if p.Positional {
			continue
		}
		short := p.Short
		if short != "" && (len(short) != 1 || usedShort[short]) {
			short = ""
		}
		usedShort[short] = short != ""
		addFlag(fs, p, short)
		if len(p.Enum) > 0 {
			enum := p.Enum
			cmd.RegisterFlagCompletionFunc(p.Flag, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
				return enum, cobra.ShellCompDirectiveNoFileComp
			})
		}
	}
	if op.Body != nil {
		fs.StringP(flagBody, "d", "", `request body: JSON/text, @file, or - for stdin (field flags are merged on top)`)
		if len(op.Body.ContentTypes) > 1 {
			fs.String(flagContentType, op.Body.ContentType, "body content type: "+strings.Join(op.Body.ContentTypes, "|"))
		}
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return a.runOperation(cmd, op, positional, args)
	}
	for _, h := range a.commandHooks {
		h(op, cmd)
	}
	return cmd
}

func positionalArgs(params []*spec.Param) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < len(params) {
			var names []string
			for _, p := range params[len(args):] {
				names = append(names, "<"+p.Flag+">")
			}
			return fmt.Errorf("missing argument(s): %s", strings.Join(names, " "))
		}
		if len(args) > len(params) {
			return fmt.Errorf("unexpected argument(s): %s", strings.Join(args[len(params):], " "))
		}
		return nil
	}
}

// resolveFlagCollisions gives every parameter of op a unique flag name that
// doesn't clash with global flags. It runs during Load so the command flags
// and the tool catalog agree on names.
func resolveFlagCollisions(op *spec.Operation) {
	used := map[string]bool{}
	for k := range reservedFlags {
		used[k] = true
	}
	for _, p := range op.AllParams() {
		p.Flag = uniqueFlag(p, used)
	}
}

func uniqueFlag(p *spec.Param, used map[string]bool) string {
	name := p.Flag
	if name == "" {
		name = naming.Kebab(p.Name)
	}
	if used[name] {
		name = string(p.In) + "-" + name
	}
	base := name
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	used[name] = true
	return name
}

func addFlag(fs *pflag.FlagSet, p *spec.Param, short string) {
	desc := p.Description
	var notes []string
	if p.Required {
		notes = append(notes, "required")
	}
	if len(p.Enum) > 0 {
		notes = append(notes, "one of: "+strings.Join(p.Enum, "|"))
	}
	if p.Type == spec.TypeObject {
		notes = append(notes, "JSON")
	}
	if p.Default != nil {
		notes = append(notes, fmt.Sprintf("server default: %v", p.Default))
	}
	if p.Preset != nil {
		notes = append(notes, fmt.Sprintf("default: %v", p.Preset))
	}
	if p.EnvVar != "" {
		notes = append(notes, "env: "+p.EnvVar)
	}
	if len(notes) > 0 {
		desc = strings.TrimSpace(desc + " (" + strings.Join(notes, "; ") + ")")
	}

	switch p.Type {
	case spec.TypeInteger:
		fs.Int64P(p.Flag, short, 0, desc)
	case spec.TypeNumber:
		fs.Float64P(p.Flag, short, 0, desc)
	case spec.TypeBoolean:
		fs.BoolP(p.Flag, short, false, desc)
	case spec.TypeArray:
		fs.StringSliceP(p.Flag, short, nil, desc)
	default:
		fs.StringP(p.Flag, short, "", desc)
	}
	if p.Hidden {
		fs.MarkHidden(p.Flag)
	}
	if p.Deprecated {
		fs.MarkDeprecated(p.Flag, "deprecated by the API")
	}
}

func (a *App) runOperation(cmd *cobra.Command, op *spec.Operation, positional []*spec.Param, args []string) error {
	in, err := inputFromFlags(cmd, op, positional, args)
	if err != nil {
		return err
	}
	return a.execute(cmd, op, in)
}

// inputFromFlags collects request values from positional args and flags,
// falling back to env vars and presets.
func inputFromFlags(cmd *cobra.Command, op *spec.Operation, positional []*spec.Param, args []string) (request.Input, error) {
	fs := cmd.Flags()
	in := request.Input{Values: map[*spec.Param]any{}}
	for i, p := range positional {
		v, err := parseValue(p, args[i])
		if err != nil {
			return in, err
		}
		in.Values[p] = v
	}
	var missing []string
	for _, p := range op.AllParams() {
		if p.Positional {
			continue
		}
		v, ok, err := resolveValue(fs, p)
		if err != nil {
			return in, err
		}
		if ok {
			in.Values[p] = v
		} else if p.Required && p.In != spec.InBody {
			missing = append(missing, "--"+p.Flag)
		}
	}
	if len(missing) > 0 {
		return in, fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}

	if op.Body != nil {
		raw, _ := fs.GetString(flagBody)
		body, err := readBodyArg(raw, cmd.InOrStdin())
		if err != nil {
			return in, err
		}
		in.RawBody = body
		if fs.Lookup(flagContentType) != nil {
			in.ContentType, _ = fs.GetString(flagContentType)
		}
	}
	return in, nil
}

// execute sends the request described by in, honoring the global flags, and
// prints the response.
func (a *App) execute(cmd *cobra.Command, op *spec.Operation, in request.Input) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	fs := cmd.Flags()
	if in.Headers == nil {
		in.Headers = http.Header{}
	}
	headers, _ := fs.GetStringArray(flagHeader)
	for _, h := range headers {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			return fmt.Errorf("invalid header %q, expected \"Key: Value\"", h)
		}
		in.Headers.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}

	baseURL, err := a.resolveBaseURL(fs)
	if err != nil {
		return err
	}
	timeout, _ := fs.GetDuration(flagTimeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := request.Build(ctx, baseURL, op, in)
	if err != nil {
		return err
	}
	if err := a.applyAuth(ctx, req, op); err != nil {
		return err
	}
	for _, h := range a.requestHooks {
		if err := h(ctx, op, req); err != nil {
			return err
		}
	}

	dryRun, _ := fs.GetBool(flagDryRun)
	resp, err := a.client(fs, dryRun, cmd.OutOrStdout()).Do(req)
	if err != nil {
		return err
	}
	if dryRun {
		resp.Body.Close()
		return nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	out := &output.Response{StatusCode: resp.StatusCode, Status: resp.Status, Header: resp.Header, Body: data}
	for _, h := range a.responseHooks {
		if err := h(ctx, op, out); err != nil {
			return err
		}
	}

	if inc, _ := fs.GetBool(flagInclude); inc {
		errw := cmd.ErrOrStderr()
		p := style.For(errw)
		fmt.Fprintf(errw, "%s %s\n", p.Dim(resp.Proto), p.Status(resp.StatusCode, resp.Status))
		for _, k := range sortedKeys(resp.Header) {
			fmt.Fprintf(errw, "%s %s\n", p.Cyan(k+":"), strings.Join(resp.Header[k], ", "))
		}
		fmt.Fprintln(errw)
	}

	format, _ := fs.GetString(flagOutput)
	f, ok := a.formatters[format]
	if !ok {
		return fmt.Errorf("unknown output format %q (available: %s)", format, strings.Join(sortedKeys(a.formatters), ", "))
	}
	w := cmd.OutOrStdout()
	if resp.StatusCode >= 400 {
		w = cmd.ErrOrStderr()
	}
	if err := f.Format(w, out); err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return &HTTPError{Op: op, Status: resp.StatusCode}
	}
	return nil
}

// client wraps the configured HTTP client with middleware. With dryRun, the
// network transport is replaced by one that prints the request, as the
// middleware left it, as a curl command.
func (a *App) client(fs *pflag.FlagSet, dryRun bool, w io.Writer) *http.Client {
	base := a.httpClient
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	mws := a.middleware
	if v, _ := fs.GetBool(flagVerbose); v && !dryRun {
		mws = append(append([]transport.Middleware(nil), mws...), transport.Debug(os.Stderr))
	}
	if dryRun {
		c.Jar = nil
		c.Transport = transport.Chain(printCurl(w), mws...)
	} else {
		c.Transport = transport.Chain(base.Transport, mws...)
	}
	return &c
}

var (
	curlStart  = regexp.MustCompile(`^curl -X (\S+) ('[^']*')`)
	curlOption = regexp.MustCompile(`(?m)^  (-H|--data-raw) `)
)

// styleCurl highlights a rendered curl command.
func styleCurl(p style.Painter, s string) string {
	if !p.Enabled() {
		return s
	}
	s = curlStart.ReplaceAllStringFunc(s, func(m string) string {
		sub := curlStart.FindStringSubmatch(m)
		return p.Bold("curl") + " " + p.Dim("-X") + " " + p.Method(sub[1]) + " " + p.URL(sub[2])
	})
	s = strings.ReplaceAll(s, " \\\n", p.Gray(" \\")+"\n")
	return curlOption.ReplaceAllStringFunc(s, func(m string) string { return "  " + p.Cyan(strings.TrimSpace(m)) + " " })
}

// printCurl is a terminal RoundTripper that prints requests instead of sending them.
func printCurl(w io.Writer) http.RoundTripper {
	return transport.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		fmt.Fprintln(w, styleCurl(style.For(w), request.Curl(req, false)))
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK (dry run)",
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{}, Body: http.NoBody, Request: req,
		}, nil
	})
}

// resolveBaseURL picks --server, then $PREFIX_SERVER, then WithBaseURL, then the spec.
func (a *App) resolveBaseURL(fs *pflag.FlagSet) (string, error) {
	if v, _ := fs.GetString(flagServer); v != "" {
		return v, nil
	}
	if v := os.Getenv(a.envPrefix + "_SERVER"); v != "" {
		return v, nil
	}
	if a.baseURL != "" {
		return a.baseURL, nil
	}
	for _, s := range a.api.Servers {
		if len(s.Missing) > 0 {
			return "", fmt.Errorf("the spec's server URL %s needs a value for {%s}: pass --server or set %s_SERVER",
				s.URL, strings.Join(s.Missing, "}, {"), a.envPrefix)
		}
		if strings.HasPrefix(s.URL, "http://") || strings.HasPrefix(s.URL, "https://") {
			return s.URL, nil
		}
	}
	hint := ""
	if len(a.api.Servers) > 0 {
		hint = fmt.Sprintf(" (the spec's server %q is relative)", a.api.Servers[0].URL)
	}
	return "", fmt.Errorf("no API server URL%s: pass --server or set %s_SERVER", hint, a.envPrefix)
}

// resolveValue returns the flag value, else the env var, else the preset.
func resolveValue(fs *pflag.FlagSet, p *spec.Param) (any, bool, error) {
	if f := fs.Lookup(p.Flag); f != nil && f.Changed {
		v, err := flagValue(fs, p)
		return v, err == nil, err
	}
	return fallbackValue(p)
}

// fallbackValue returns the env var value, else the preset.
func fallbackValue(p *spec.Param) (any, bool, error) {
	if p.EnvVar != "" {
		if s, ok := os.LookupEnv(p.EnvVar); ok {
			v, err := parseValue(p, s)
			return v, err == nil, err
		}
	}
	if p.Preset != nil {
		return p.Preset, true, nil
	}
	return nil, false, nil
}

func flagValue(fs *pflag.FlagSet, p *spec.Param) (any, error) {
	switch p.Type {
	case spec.TypeInteger:
		return fs.GetInt64(p.Flag)
	case spec.TypeNumber:
		return fs.GetFloat64(p.Flag)
	case spec.TypeBoolean:
		return fs.GetBool(p.Flag)
	case spec.TypeArray:
		items, err := fs.GetStringSlice(p.Flag)
		if err != nil {
			return nil, err
		}
		return parseArray(p, items)
	}
	s, err := fs.GetString(p.Flag)
	if err != nil {
		return nil, err
	}
	return parseValue(p, s)
}

// parseValue converts a string (positional arg or env var) to the param's type.
func parseValue(p *spec.Param, s string) (any, error) {
	switch p.Type {
	case spec.TypeArray:
		return parseArray(p, strings.Split(s, ","))
	case spec.TypeObject:
		if p.In != spec.InBody {
			return s, nil
		}
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("--%s must be valid JSON: %w", p.Flag, err)
		}
		return v, nil
	}
	v, err := parseScalar(p.Type, s)
	if err != nil {
		return nil, fmt.Errorf("--%s: %w", p.Flag, err)
	}
	if err := checkEnum(p, s); err != nil {
		return nil, err
	}
	return v, nil
}

func parseArray(p *spec.Param, items []string) (any, error) {
	out := make([]any, 0, len(items))
	for _, s := range items {
		v, err := parseScalar(p.ItemType, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", p.Flag, err)
		}
		if err := checkEnum(p, s); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func parseScalar(t spec.Type, s string) (any, error) {
	switch t {
	case spec.TypeInteger:
		return strconv.ParseInt(s, 10, 64)
	case spec.TypeNumber:
		return strconv.ParseFloat(s, 64)
	case spec.TypeBoolean:
		return strconv.ParseBool(s)
	}
	return s, nil
}

func checkEnum(p *spec.Param, s string) error {
	if len(p.Enum) == 0 {
		return nil
	}
	for _, e := range p.Enum {
		if e == s {
			return nil
		}
	}
	return fmt.Errorf("invalid value %q for %s (one of: %s)", s, p.Flag, strings.Join(p.Enum, ", "))
}

// readBodyArg interprets --body: "" means none, "-" reads stdin, "@path" reads a file.
func readBodyArg(v string, stdin io.Reader) ([]byte, error) {
	switch {
	case v == "":
		return nil, nil
	case v == "-":
		b, err := io.ReadAll(stdin)
		return bytes.TrimRight(b, "\r\n"), err
	case strings.HasPrefix(v, "@"):
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("--body: %w", err)
		}
		return bytes.TrimRight(b, "\r\n"), nil
	}
	return []byte(v), nil
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if i := strings.Index(s, ". "); i > 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, ".")
}

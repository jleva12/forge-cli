// Package catalog describes the exposed operations as machine-readable tool
// definitions for LLM agents: a name, a description, a JSON Schema for the
// input and a summary of the responses.
//
// Input schema properties are keyed by flag name, so a tool call's input maps
// one-to-one onto "<cli> call <tool> '<json>'" and onto the command's flags.
package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"forge-cli/spec"
)

// BodyProperty is the input property carrying a complete request body. It's
// offered when the body can't be expressed as individual fields.
const BodyProperty = "body"

// Tool is one callable operation.
type Tool struct {
	Name        string         `json:"name"`
	Command     string         `json:"command"`
	Usage       string         `json:"usage"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description"`
	Deprecated  bool           `json:"deprecated,omitempty"`
	Auth        [][]string     `json:"auth,omitempty"` // alternatives; each lists the schemes it needs together
	InputSchema map[string]any `json:"input_schema"`
	Responses   []Response     `json:"responses,omitempty"`

	Op *spec.Operation `json:"-"`
}

// Response summarizes one documented response.
type Response struct {
	Status      string         `json:"status"`
	Description string         `json:"description,omitempty"`
	ContentType string         `json:"content_type,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
}

// Options controls catalog generation.
type Options struct {
	// CLIName prefixes commands and usage strings.
	CLIName string
	// MaxSchemaDepth bounds how deeply nested schemas are expanded (default 4).
	MaxSchemaDepth int
	// IncludeHidden includes hidden commands and parameters.
	IncludeHidden bool
	// ResponseSchemas includes the schema of each operation's success response.
	ResponseSchemas bool
	// ResponseSchemaDepth bounds response schema nesting separately (default 2):
	// response objects are often huge, and field names are usually what an
	// agent needs to chain calls.
	ResponseSchemaDepth int
}

// Build creates a tool for every operation.
func Build(ops []*spec.Operation, opts Options) []Tool {
	if opts.MaxSchemaDepth <= 0 {
		opts.MaxSchemaDepth = 4
	}
	if opts.ResponseSchemaDepth <= 0 {
		opts.ResponseSchemaDepth = 2
	}
	used := map[string]bool{}
	var tools []Tool
	for _, op := range ops {
		if op.Hidden && !opts.IncludeHidden {
			continue
		}
		name := ToolName(op)
		base := name
		for i := 2; used[name]; i++ {
			suffix := fmt.Sprintf("_%d", i)
			name = truncate(base, 64-len(suffix)) + suffix
		}
		used[name] = true
		tools = append(tools, buildTool(op, name, opts))
	}
	return tools
}

var (
	nonToolChars = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
)

// CleanText strips HTML tags (common in generated specs) and excess blank lines.
func CleanText(s string) string { return spec.CleanText(s) }

// ToolName derives a name valid for LLM tool APIs (^[a-zA-Z0-9_-]{1,64}$)
// from the command path: "pet list" -> "pet_list".
func ToolName(op *spec.Operation) string {
	name := strings.Trim(nonToolChars.ReplaceAllString(op.CommandPath(), "_"), "_")
	if name == "" {
		name = "operation"
	}
	return truncate(strings.ToLower(name), 64)
}

// NormalizeName makes user-typed names comparable with tool names, so
// "pet list", "pet-list" and "pet_list" all match.
func NormalizeName(s string) string {
	return strings.Trim(nonToolChars.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func buildTool(op *spec.Operation, name string, opts Options) Tool {
	command := strings.TrimSpace(opts.CLIName + " " + op.CommandPath())
	t := Tool{
		Name: name, Command: command, Method: op.Method, Path: op.Path,
		Summary: op.Summary, Deprecated: op.Deprecated, Op: op,
		Description: description(op),
		InputSchema: inputSchema(op, opts),
		Usage:       Synopsis(op, command, SynopsisOptions{IncludeHidden: opts.IncludeHidden}),
	}
	for _, req := range op.Security {
		if len(req) == 0 {
			continue // anonymous alternative
		}
		var schemes []string
		for s := range req {
			schemes = append(schemes, s)
		}
		t.Auth = append(t.Auth, sortStrings(schemes))
	}
	for _, r := range op.Responses {
		t.Responses = append(t.Responses, Response{Status: r.Status, Description: r.Description, ContentType: r.ContentType})
	}
	if opts.ResponseSchemas {
		AddResponseSchemas([]Tool{t}, opts.ResponseSchemaDepth)
	}
	return t
}

// AddResponseSchemas fills in success response schemas, e.g. only for the few
// tools an agent asked about.
func AddResponseSchemas(tools []Tool, depth int) {
	for _, t := range tools {
		for i, r := range t.Op.Responses {
			if r.Schema != nil && strings.HasPrefix(r.Status, "2") && i < len(t.Responses) {
				t.Responses[i].Schema = Schema(r.Schema, depth, false)
			}
		}
	}
}

// description composes everything an agent needs to decide when to call the tool.
func description(op *spec.Operation) string {
	var parts []string
	if op.Summary != "" {
		parts = append(parts, strings.TrimSuffix(op.Summary, ".")+".")
	}
	if d := CleanText(op.Description); d != "" && d != op.Summary {
		parts = append(parts, d)
	}
	if len(parts) == 0 {
		parts = append(parts, "Calls "+op.Key()+".")
	}
	if ok := op.Success(); ok != nil && informative(ok.Description) {
		parts = append(parts, "Returns: "+ok.Description)
	}
	if op.Deprecated {
		parts = append(parts, "Deprecated: prefer an alternative if one exists.")
	}
	return strings.Join(parts, "\n\n")
}

// informative filters out boilerplate response descriptions such as "OK" or
// "Successful operation" that tell an agent nothing.
func informative(desc string) bool {
	return len(strings.Fields(desc)) > 2
}

func inputSchema(op *spec.Operation, opts Options) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range op.AllParams() {
		if p.Hidden && !opts.IncludeHidden {
			continue
		}
		props[p.Flag] = paramSchema(p, opts.MaxSchemaDepth)
		if isRequired(op, p) {
			required = append(required, p.Flag)
		}
	}
	if b := op.Body; b != nil && len(b.Fields) == 0 && !emptyOptionalBody(b) {
		var s map[string]any
		if b.Schema != nil && strings.Contains(b.ContentType, "json") {
			s = Schema(b.Schema, opts.MaxSchemaDepth, true)
		} else {
			s = map[string]any{"type": "string"}
		}
		s["description"] = strings.TrimSpace(fmt.Sprintf("Request body (%s). %s", b.ContentType, b.Description))
		props[BodyProperty] = s
		if b.Required {
			required = append(required, BodyProperty)
		}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// emptyOptionalBody reports an optional body whose schema is an object that
// can't hold anything, which some generated specs declare even on GETs.
func emptyOptionalBody(b *spec.Body) bool {
	if b.Required || b.Schema == nil || len(b.Schema.Properties) > 0 || len(b.Schema.AllOf)+len(b.Schema.OneOf)+len(b.Schema.AnyOf) > 0 {
		return false
	}
	ap := b.Schema.AdditionalProperties
	return b.Schema.Type != nil && b.Schema.Type.Is("object") && ap.Schema == nil && (ap.Has == nil || !*ap.Has)
}

func paramSchema(p *spec.Param, depth int) map[string]any {
	var s map[string]any
	switch p.Type {
	case spec.TypeObject:
		if p.In == spec.InBody && p.Schema != nil {
			s = Schema(p.Schema, depth, true)
		} else {
			s = map[string]any{"type": "string", "contentMediaType": "application/json"}
		}
	case spec.TypeArray:
		items := map[string]any{"type": jsonType(p.ItemType)}
		if len(p.Enum) > 0 {
			items["enum"] = p.Enum
		}
		s = map[string]any{"type": "array", "items": items}
	default:
		s = map[string]any{"type": jsonType(p.Type)}
		if len(p.Enum) > 0 {
			s["enum"] = enumValues(p.Type, p.Enum)
		}
	}
	if p.Format != "" && s["format"] == nil {
		s["format"] = p.Format
	}
	var notes []string
	if d := CleanText(p.Description); d != "" {
		notes = append(notes, strings.TrimSuffix(d, "."))
	}
	if p.Format == "binary" && p.In == spec.InBody {
		notes = append(notes, "Path to a local file to upload")
	}
	if p.In == spec.InHeader || p.In == spec.InCookie {
		notes = append(notes, "Sent as "+string(p.In)+" "+p.Name)
	}
	if p.Preset != nil {
		notes = append(notes, fmt.Sprintf("Defaults to %v", p.Preset))
	}
	if p.EnvVar != "" {
		notes = append(notes, "Falls back to $"+p.EnvVar)
	}
	if p.Deprecated {
		notes = append(notes, "Deprecated")
	}
	if len(notes) > 0 {
		s["description"] = strings.Join(notes, ". ") + "."
	}
	if p.Default != nil {
		s["default"] = p.Default
	}
	return s
}

// Schema converts an OpenAPI schema into a self-contained JSON Schema: $refs
// are inlined, recursion is cut off, and nesting is limited to depth. With
// forInput, readOnly properties are dropped; otherwise writeOnly ones are.
func Schema(s *openapi3.Schema, depth int, forInput bool) map[string]any {
	return convert(s, depth, forInput, map[*openapi3.Schema]bool{})
}

func convert(s *openapi3.Schema, depth int, forInput bool, seen map[*openapi3.Schema]bool) map[string]any {
	out := map[string]any{}
	if s == nil {
		return out
	}
	if t := schemaTypes(s); len(t) == 1 {
		out["type"] = t[0]
	} else if len(t) > 1 {
		out["type"] = t
	}
	switch {
	case seen[s]:
		out["description"] = strings.TrimSpace(CleanText(s.Description) + " (recursive: same shape as an enclosing object)")
		return out
	case depth <= 0 && isComplex(s):
		out["description"] = strings.TrimSpace(CleanText(s.Description) + " (nested structure omitted)")
		return out
	}
	seen[s] = true
	defer delete(seen, s)

	if d := CleanText(s.Description); d != "" {
		out["description"] = d
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	if s.Nullable {
		out["nullable"] = true
	}
	if s.Min != nil {
		out["minimum"] = *s.Min
	}
	if s.Max != nil {
		out["maximum"] = *s.Max
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}

	// Merge allOf object compositions into one object.
	props := map[string]*openapi3.SchemaRef{}
	var required []string
	var merge func(*openapi3.Schema)
	merge = func(x *openapi3.Schema) {
		for _, sub := range x.AllOf {
			if sub != nil && sub.Value != nil {
				merge(sub.Value)
			}
		}
		for k, v := range x.Properties {
			props[k] = v
		}
		required = append(required, x.Required...)
	}
	merge(s)
	if len(props) > 0 {
		if out["type"] == nil {
			out["type"] = "object"
		}
		pm := map[string]any{}
		for name, ref := range props {
			if ref == nil || ref.Value == nil {
				continue
			}
			if (forInput && ref.Value.ReadOnly) || (!forInput && ref.Value.WriteOnly) {
				continue
			}
			pm[name] = convert(ref.Value, depth-1, forInput, seen)
		}
		out["properties"] = pm
		var req []string
		for _, r := range required {
			if _, ok := pm[r]; ok {
				req = append(req, r)
			}
		}
		if len(req) > 0 {
			out["required"] = sortStrings(dedupe(req))
		}
	}
	if ap := s.AdditionalProperties; ap.Schema != nil && ap.Schema.Value != nil {
		out["additionalProperties"] = convert(ap.Schema.Value, depth-1, forInput, seen)
	} else if ap.Has != nil {
		out["additionalProperties"] = *ap.Has
	}
	if s.Items != nil && s.Items.Value != nil {
		out["items"] = convert(s.Items.Value, depth-1, forInput, seen)
	}
	for key, refs := range map[string]openapi3.SchemaRefs{"oneOf": s.OneOf, "anyOf": s.AnyOf} {
		if len(refs) == 0 {
			continue
		}
		var list []any
		for _, r := range refs {
			if r != nil && r.Value != nil {
				list = append(list, convert(r.Value, depth-1, forInput, seen))
			}
		}
		out[key] = list
	}
	return out
}

func isComplex(s *openapi3.Schema) bool {
	return len(s.Properties) > 0 || s.Items != nil || len(s.AllOf)+len(s.OneOf)+len(s.AnyOf) > 0 ||
		s.AdditionalProperties.Schema != nil
}

func schemaTypes(s *openapi3.Schema) []string {
	if s.Type == nil {
		return nil
	}
	return s.Type.Slice()
}

func jsonType(t spec.Type) string {
	if t == "" {
		return "string"
	}
	return string(t)
}

func enumValues(t spec.Type, vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
		switch t {
		case spec.TypeInteger:
			var n int64
			if _, err := fmt.Sscan(v, &n); err == nil {
				out[i] = n
			}
		case spec.TypeNumber:
			var f float64
			if _, err := fmt.Sscan(v, &f); err == nil {
				out[i] = f
			}
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortStrings(ss []string) []string {
	sort.Strings(ss)
	return ss
}

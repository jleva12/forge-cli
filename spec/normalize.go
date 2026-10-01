package spec

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// NormalizeOptions controls how the OpenAPI document is mapped onto the model.
type NormalizeOptions struct {
	// BodyFlattenDepth is how many levels of nested object properties become
	// dotted flags (e.g. --owner.name). 0 means the default of 2; a negative
	// value exposes top-level properties only.
	BodyFlattenDepth int
	// ServerVariables fill in variables in the servers' URLs, such as
	// {tenant} in https://{tenant}.example.com, instead of their defaults.
	ServerVariables map[string]string
}

var methodOrder = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace, "QUERY",
}

// Normalize converts a parsed OpenAPI document into the CLI model.
func Normalize(doc *openapi3.T, opts NormalizeOptions) (*API, error) {
	depth := opts.BodyFlattenDepth
	switch {
	case depth == 0:
		depth = 2
	case depth < 0:
		depth = 1
	}

	api := &API{
		Tags:            map[string]string{},
		SecuritySchemes: map[string]*SecurityScheme{},
		Doc:             doc,
	}
	if doc.Info != nil {
		api.Title, api.Version, api.Description = doc.Info.Title, doc.Info.Version, doc.Info.Description
	}
	if err := checkServerVariables(doc.Servers, opts.ServerVariables); err != nil {
		return nil, err
	}
	for _, s := range doc.Servers {
		if s != nil {
			api.Servers = append(api.Servers, expandServer(s, opts.ServerVariables))
		}
	}
	for _, t := range doc.Tags {
		if t != nil {
			api.Tags[t.Name] = t.Description
		}
	}
	if doc.Components != nil {
		for name, ref := range doc.Components.SecuritySchemes {
			if ref == nil || ref.Value == nil {
				continue
			}
			s := ref.Value
			api.SecuritySchemes[name] = &SecurityScheme{
				Name: name, Type: s.Type, Scheme: strings.ToLower(s.Scheme), BearerFormat: s.BearerFormat,
				In: Location(s.In), ParamName: s.Name, Description: s.Description,
				Flows: s.Flows, OpenIDConnectURL: s.OpenIdConnectUrl,
			}
		}
	}
	global := convertSecurity(doc.Security)

	if doc.Paths == nil {
		return api, nil
	}
	paths := doc.Paths.Map()
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, path := range keys {
		item := paths[path]
		if item == nil {
			continue
		}
		ops := item.Operations()
		for _, method := range orderedMethods(ops) {
			raw := ops[method]
			if raw == nil {
				continue
			}
			op, err := normalizeOperation(path, strings.ToUpper(method), item, raw, depth)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			if raw.Security != nil {
				op.Security = convertSecurity(*raw.Security)
			} else {
				op.Security = global
			}
			api.Operations = append(api.Operations, op)
		}
	}
	return api, nil
}

func orderedMethods(ops map[string]*openapi3.Operation) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range methodOrder {
		if _, ok := ops[m]; ok {
			out = append(out, m)
			seen[m] = true
		}
	}
	var rest []string
	for m := range ops {
		if !seen[m] {
			rest = append(rest, m)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func normalizeOperation(path, method string, item *openapi3.PathItem, raw *openapi3.Operation, depth int) (*Operation, error) {
	op := &Operation{
		ID: raw.OperationID, Method: method, Path: path,
		Summary: firstLine(raw.Summary), Description: raw.Description,
		Tags: raw.Tags, Deprecated: raw.Deprecated,
		Extensions: raw.Extensions, Raw: raw,
	}
	if op.Summary == "" {
		op.Summary = firstLine(raw.Description)
	}

	// Operation-level parameters override path-level ones with the same name+location.
	type key struct{ name, in string }
	var order []key
	byKey := map[key]*openapi3.Parameter{}
	for _, refs := range []openapi3.Parameters{item.Parameters, raw.Parameters} {
		for _, ref := range refs {
			if ref == nil || ref.Value == nil {
				continue
			}
			k := key{ref.Value.Name, ref.Value.In}
			if _, ok := byKey[k]; !ok {
				order = append(order, k)
			}
			byKey[k] = ref.Value
		}
	}
	for _, k := range order {
		op.Params = append(op.Params, normalizeParam(byKey[k]))
	}

	if raw.RequestBody != nil && raw.RequestBody.Value != nil {
		op.Body = normalizeBody(raw.RequestBody.Value, depth)
	}
	if raw.Responses != nil {
		op.Responses = normalizeResponses(raw.Responses)
	}
	return op, nil
}

func normalizeResponses(rs *openapi3.Responses) []*Response {
	var out []*Response
	for status, ref := range rs.Map() {
		if ref == nil || ref.Value == nil {
			continue
		}
		r := &Response{Status: status}
		if ref.Value.Description != nil {
			r.Description = firstLine(*ref.Value.Description)
		}
		var cts []string
		for ct := range ref.Value.Content {
			cts = append(cts, ct)
		}
		sort.Strings(cts)
		if r.ContentType = preferredContentType(cts); r.ContentType != "" {
			r.Schema = schemaOf(ref.Value.Content[r.ContentType].Schema)
		}
		out = append(out, r)
	}
	// "~" sorts after digits and "NXX" ranges, so "default" comes last.
	key := func(status string) string { return strings.Replace(status, "default", "~", 1) }
	sort.Slice(out, func(i, j int) bool { return key(out[i].Status) < key(out[j].Status) })
	return out
}

func normalizeParam(p *openapi3.Parameter) *Param {
	schema := schemaOf(p.Schema)
	if schema == nil {
		for _, mt := range p.Content {
			if mt != nil {
				schema = schemaOf(mt.Schema)
				break
			}
		}
	}
	out := &Param{
		Name: p.Name, In: Location(p.In), Description: firstLine(p.Description),
		Required: p.Required || p.In == openapi3.ParameterInPath, Deprecated: p.Deprecated,
		Extensions: p.Extensions,
	}
	applySchema(out, schema)
	return out
}

func normalizeBody(rb *openapi3.RequestBody, depth int) *Body {
	body := &Body{Required: rb.Required, Description: rb.Description}
	for ct := range rb.Content {
		body.ContentTypes = append(body.ContentTypes, ct)
	}
	sort.Strings(body.ContentTypes)
	body.ContentType = preferredContentType(body.ContentTypes)
	if mt := rb.Content.Get(body.ContentType); mt != nil {
		body.Schema = schemaOf(mt.Schema)
	}
	if body.Schema != nil && schemaType(body.Schema) == TypeObject {
		body.Fields = flattenProperties(body.Schema, nil, depth, true)
	}
	if strings.HasPrefix(body.ContentType, "multipart/") {
		for _, f := range body.Fields {
			markFileField(f)
		}
	}
	return body
}

// markFileField gives OpenAPI 3.1 file parts the format OpenAPI 3.0 uses
// for them. 3.1 marks a file as a string with a contentMediaType, plus
// contentEncoding: base64 when it's encoded; 3.0 used format binary or byte.
// Outside multipart bodies, contentMediaType only describes a string's text.
func markFileField(p *Param) {
	s := p.Schema
	if s == nil || p.Format != "" || p.Type != TypeString || s.ContentMediaType == "" {
		return
	}
	p.Format = "binary"
	if strings.EqualFold(s.ContentEncoding, "base64") {
		p.Format = "byte"
	}
}

// flattenProperties turns object properties into flag-able fields. Nested
// objects become dotted names up to depth levels; deeper or complex values
// are exposed as JSON-string fields.
func flattenProperties(s *openapi3.Schema, prefix []string, depth int, parentRequired bool) []*Param {
	props, required := mergedProperties(s)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []*Param
	for _, name := range names {
		ps := schemaOf(props[name])
		if ps == nil || ps.ReadOnly {
			continue
		}
		path := append(append([]string(nil), prefix...), name)
		isRequired := parentRequired && required[name]
		if schemaType(ps) == TypeObject && len(path) < depth {
			if sub, _ := mergedProperties(ps); len(sub) > 0 {
				out = append(out, flattenProperties(ps, path, depth, isRequired)...)
				continue
			}
		}
		p := &Param{
			Name: strings.Join(path, "."), In: InBody, BodyPath: path,
			Description: firstLine(ps.Description), Required: isRequired,
			Deprecated: ps.Deprecated, Extensions: ps.Extensions,
		}
		applySchema(p, ps)
		out = append(out, p)
	}
	return out
}

// mergedProperties collects properties across allOf compositions.
func mergedProperties(s *openapi3.Schema) (map[string]*openapi3.SchemaRef, map[string]bool) {
	props := map[string]*openapi3.SchemaRef{}
	required := map[string]bool{}
	var walk func(*openapi3.Schema)
	walk = func(s *openapi3.Schema) {
		if s == nil {
			return
		}
		for _, sub := range s.AllOf {
			walk(schemaOf(sub))
		}
		for k, v := range s.Properties {
			props[k] = v
		}
		for _, r := range s.Required {
			required[r] = true
		}
	}
	walk(s)
	return props, required
}

func applySchema(p *Param, s *openapi3.Schema) {
	p.Type = schemaType(s)
	p.Schema = s
	if s == nil {
		return
	}
	p.Format = s.Format
	p.Default = s.Default
	if p.Description == "" {
		p.Description = firstLine(s.Description)
	}
	p.Enum = enumStrings(s.Enum)
	if p.Type == TypeArray {
		items := schemaOf(s.Items)
		p.ItemType = schemaType(items)
		if items != nil && len(p.Enum) == 0 {
			p.Enum = enumStrings(items.Enum)
		}
		if p.ItemType == TypeArray || p.ItemType == TypeObject {
			// Arrays of complex values are passed as a JSON string.
			p.Type, p.ItemType = TypeObject, ""
		}
	}
}

func schemaOf(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func schemaType(s *openapi3.Schema) Type {
	if s == nil {
		return TypeString
	}
	if s.Type != nil {
		for _, t := range s.Type.Slice() {
			if t != "null" {
				return Type(t)
			}
		}
	}
	switch {
	case len(s.AllOf) == 1:
		return schemaType(schemaOf(s.AllOf[0]))
	case len(s.Properties) > 0 || len(s.AllOf) > 1:
		return TypeObject
	case s.Items != nil:
		return TypeArray
	case len(s.OneOf) > 0 || len(s.AnyOf) > 0:
		return TypeObject
	}
	return TypeString
}

func preferredContentType(cts []string) string {
	for _, want := range []func(string) bool{
		func(ct string) bool { return ct == "application/json" },
		func(ct string) bool { return strings.HasSuffix(ct, "+json") || strings.Contains(ct, "json") },
		func(ct string) bool { return ct == "application/x-www-form-urlencoded" },
		func(ct string) bool { return ct == "multipart/form-data" },
	} {
		for _, ct := range cts {
			if want(ct) {
				return ct
			}
		}
	}
	if len(cts) > 0 {
		return cts[0]
	}
	return ""
}

var serverVariable = regexp.MustCompile(`\{([^{}]+)\}`)

// expandServer fills in a server URL's variables from vars, else from their
// defaults.
func expandServer(s *openapi3.Server, vars map[string]string) Server {
	out := Server{URL: s.URL, Description: s.Description}
	for name, value := range vars {
		out.URL = strings.ReplaceAll(out.URL, "{"+name+"}", value)
	}
	for name, v := range s.Variables {
		if v != nil {
			out.URL = strings.ReplaceAll(out.URL, "{"+name+"}", v.Default)
		}
	}
	for _, m := range serverVariable.FindAllStringSubmatch(out.URL, -1) {
		out.Missing = append(out.Missing, m[1])
	}
	return out
}

// checkServerVariables rejects values for variables that no server URL has,
// and values outside a variable's enum.
func checkServerVariables(servers openapi3.Servers, vars map[string]string) error {
	if len(vars) == 0 {
		return nil
	}
	known := map[string]*openapi3.ServerVariable{}
	for _, s := range servers {
		if s == nil {
			continue
		}
		for _, m := range serverVariable.FindAllStringSubmatch(s.URL, -1) {
			if known[m[1]] == nil {
				known[m[1]] = s.Variables[m[1]]
			}
		}
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, ok := known[name]
		if !ok {
			if len(known) == 0 {
				return fmt.Errorf("server variable %q: the spec's server URLs have no variables", name)
			}
			have := make([]string, 0, len(known))
			for k := range known {
				have = append(have, k)
			}
			sort.Strings(have)
			return fmt.Errorf("server variable %q: the spec's server URLs have no such variable (they have: %s)", name, strings.Join(have, ", "))
		}
		if v != nil && len(v.Enum) > 0 && !slices.Contains(v.Enum, vars[name]) {
			return fmt.Errorf("server variable %s=%q: must be one of %s", name, vars[name], strings.Join(v.Enum, ", "))
		}
	}
	return nil
}

func convertSecurity(reqs openapi3.SecurityRequirements) []SecurityRequirement {
	if reqs == nil {
		return nil
	}
	out := make([]SecurityRequirement, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, SecurityRequirement(r))
	}
	return out
}

func enumStrings(vals []any) []string {
	var out []string
	for _, v := range vals {
		if v != nil {
			out = append(out, fmt.Sprint(v))
		}
	}
	return out
}

var (
	htmlTags   = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	blankLines = regexp.MustCompile(`\n{3,}`)
)

// CleanText strips HTML tags (common in generated specs) and excess blank lines.
func CleanText(s string) string {
	s = htmlTags.ReplaceAllString(s, "")
	s = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&amp;", "&").Replace(s)
	return strings.TrimSpace(blankLines.ReplaceAllString(s, "\n\n"))
}

func firstLine(s string) string {
	s = CleanText(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// Package naming decides the command, group and flag names generated for an API.
package naming

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/jleva12/forge-cli/spec"
)

// Namer derives CLI names from operations. Implement it to change naming
// wholesale; use transforms to adjust individual names.
type Namer interface {
	// Group returns the parent command for an operation, or "" for the root.
	Group(op *spec.Operation) string
	// Command returns the operation's command name.
	Command(op *spec.Operation) string
	// Flag returns the flag name for a parameter or body field.
	Flag(op *spec.Operation, p *spec.Param) string
}

// Default is the standard Namer:
//   - group: first tag, else the first meaningful path segment
//   - command: operationId, else method + path (GET /v1/pets/{id} -> get-pets-by-id)
//   - flag: the parameter name; nested body fields are dotted (owner.name)
//
// Everything is kebab-cased.
type Default struct {
	// IgnoreTags groups by path segment even when tags are present.
	IgnoreTags bool
	// SkipSegments are path segments ignored when grouping by path.
	// Defaults to "api" and version segments such as "v1".
	SkipSegments []string
}

var versionSegment = regexp.MustCompile(`^v\d+(\.\d+)*$`)

func (d Default) Group(op *spec.Operation) string {
	if !d.IgnoreTags && len(op.Tags) > 0 {
		return Kebab(op.Tags[0])
	}
	return Kebab(PathSegment(op.Path, 0, d.SkipSegments...))
}

func (d Default) Command(op *spec.Operation) string {
	if op.ID != "" {
		return Kebab(op.ID)
	}
	parts := []string{strings.ToLower(op.Method)}
	var params []string
	for _, seg := range strings.Split(op.Path, "/") {
		switch {
		case seg == "" || d.skip(seg):
		case strings.HasPrefix(seg, "{"):
			params = append(params, strings.Trim(seg, "{}"))
		default:
			parts = append(parts, seg)
		}
	}
	if len(params) > 0 {
		parts = append(parts, "by", strings.Join(params, "-and-"))
	}
	return Kebab(strings.Join(parts, "-"))
}

func (d Default) Flag(_ *spec.Operation, p *spec.Param) string {
	if len(p.BodyPath) > 0 {
		segs := make([]string, len(p.BodyPath))
		for i, s := range p.BodyPath {
			segs[i] = Kebab(s)
		}
		return strings.Join(segs, ".")
	}
	return Kebab(p.Name)
}

func (d Default) skip(seg string) bool {
	skip := d.SkipSegments
	if skip == nil {
		skip = []string{"api"}
	}
	return versionSegment.MatchString(seg) || contains(skip, seg)
}

// PathSegment returns the n-th static path segment (0-based), skipping
// parameters, "api" and version segments (or the given skip list).
func PathSegment(path string, n int, skip ...string) string {
	if skip == nil {
		skip = []string{"api"}
	}
	i := 0
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || strings.HasPrefix(seg, "{") || versionSegment.MatchString(seg) || contains(skip, seg) {
			continue
		}
		if i == n {
			return seg
		}
		i++
	}
	return ""
}

// Kebab converts camelCase, snake_case, dotted and spaced names to kebab-case.
// "getPetByID" -> "get-pet-by-id", "HTTPServer" -> "http-server".
func Kebab(s string) string {
	runes := []rune(strings.TrimSpace(s))
	var b strings.Builder
	dash := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	for i, r := range runes {
		switch {
		case unicode.IsUpper(r):
			prevLower := i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]))
			acronymEnd := i > 0 && unicode.IsUpper(runes[i-1]) && i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if prevLower || acronymEnd {
				dash()
			}
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			dash()
		}
	}
	return strings.Trim(b.String(), "-")
}

// EnvName converts a name to an UPPER_SNAKE environment variable name.
func EnvName(parts ...string) string {
	var segs []string
	for _, p := range parts {
		if k := Kebab(p); k != "" {
			segs = append(segs, strings.ToUpper(strings.ReplaceAll(k, "-", "_")))
		}
	}
	return strings.Join(segs, "_")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

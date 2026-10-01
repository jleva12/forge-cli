package catalog

import (
	"strings"

	"github.com/jleva12/forge-cli/spec"
)

// SynopsisOptions controls Synopsis output.
type SynopsisOptions struct {
	// RequiredOnly omits optional flags.
	RequiredOnly bool
	// IncludeHidden includes hidden flags.
	IncludeHidden bool
	// MaxEnum is the most enum values spelled out before falling back to the
	// type name (default 6).
	MaxEnum int
}

// Synopsis renders a complete invocation template for op, e.g.
//
//	petstore pet get <pet-id>
//	petstore pet list [--limit <integer>] [--status <available|pending|sold>,...]
//	petstore pet create --name <string> [--owner.name <string>] [-d <json|@file|->]
//
// Required arguments are bare and optional ones are in brackets.
func Synopsis(op *spec.Operation, command string, opts SynopsisOptions) string {
	if opts.MaxEnum <= 0 {
		opts.MaxEnum = 6
	}
	parts := []string{command}
	for _, p := range op.Params {
		if p.Positional {
			parts = append(parts, "<"+p.Flag+">")
		}
	}
	var optional []string
	for _, p := range op.AllParams() {
		if p.Positional || (p.Hidden && !opts.IncludeHidden) {
			continue
		}
		arg := "--" + p.Flag
		if p.Type != spec.TypeBoolean {
			arg += " " + placeholder(p, opts.MaxEnum)
		}
		if isRequired(op, p) {
			parts = append(parts, arg)
		} else {
			optional = append(optional, "["+arg+"]")
		}
	}
	if b := op.Body; b != nil && !emptyOptionalBody(b) {
		arg := "-d <json|@file|->"
		if !strings.Contains(b.ContentType, "json") {
			arg = "-d <data|@file|->"
		}
		if b.Required && len(b.Fields) == 0 {
			parts = append(parts, arg)
		} else {
			optional = append(optional, "["+arg+"]")
		}
	}
	if !opts.RequiredOnly {
		parts = append(parts, optional...)
	}
	return strings.Join(parts, " ")
}

// isRequired reports whether the user must supply p: required by the spec and
// with no preset or environment fallback. Body fields count only when the
// body itself is required.
func isRequired(op *spec.Operation, p *spec.Param) bool {
	if !p.Required || p.Preset != nil || p.EnvVar != "" {
		return false
	}
	return p.In != spec.InBody || (op.Body != nil && op.Body.Required)
}

func placeholder(p *spec.Param, maxEnum int) string {
	value := func(t spec.Type) string {
		switch {
		case p.Format == "binary" && p.In == spec.InBody:
			return "<file>"
		case len(p.Enum) > 0 && len(p.Enum) <= maxEnum:
			return "<" + strings.Join(p.Enum, "|") + ">"
		case t == spec.TypeObject:
			return "<json>"
		case t == "":
			return "<string>"
		}
		return "<" + string(t) + ">"
	}
	if p.Type == spec.TypeArray {
		return value(p.ItemType) + ",..."
	}
	return value(p.Type)
}

// Package transform reshapes operations after naming and filtering: renaming
// commands, regrouping, hiding or presetting parameters, and so on.
//
// Transforms apply to every exposed operation; scope them with When:
//
//	transform.When(filter.Tag("pet"), transform.TrimGroupFromName())
//	transform.When(filter.OperationID("findPetsByStatus"), transform.Rename("list"))
package transform

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/jleva12/forge-cli/filter"
	"github.com/jleva12/forge-cli/naming"
	"github.com/jleva12/forge-cli/spec"
)

// Transform mutates an operation in place.
type Transform func(op *spec.Operation)

// When applies transforms only to operations matching m.
func When(m filter.Matcher, ts ...Transform) Transform {
	return func(op *spec.Operation) {
		if m(op) {
			for _, t := range ts {
				t(op)
			}
		}
	}
}

// Chain combines transforms into one.
func Chain(ts ...Transform) Transform {
	return func(op *spec.Operation) {
		for _, t := range ts {
			t(op)
		}
	}
}

// --- Commands ---------------------------------------------------------------

// Rename sets the command name. Use with When.
func Rename(name string) Transform { return func(op *spec.Operation) { op.Name = name } }

// RenameOperation sets the command name for an operation ID.
func RenameOperation(operationID, name string) Transform {
	return When(filter.OperationID(operationID), Rename(name))
}

// SetGroup moves operations to a group ("" for the root). Use with When.
func SetGroup(group string) Transform { return func(op *spec.Operation) { op.Group = group } }

// RenameGroup renames a generated group.
func RenameGroup(from, to string) Transform {
	return func(op *spec.Operation) {
		if op.Group == from {
			op.Group = to
		}
	}
}

// Flatten puts every command directly under the root.
func Flatten() Transform { return SetGroup("") }

// GroupByPathSegment groups by the n-th static path segment (0-based),
// skipping "api" and version segments.
func GroupByPathSegment(n int) Transform {
	return func(op *spec.Operation) {
		if seg := naming.PathSegment(op.Path, n); seg != "" {
			op.Group = naming.Kebab(seg)
		}
	}
}

// Aliases adds command aliases. Use with When.
func Aliases(aliases ...string) Transform {
	return func(op *spec.Operation) { op.Aliases = append(op.Aliases, aliases...) }
}

// Hide keeps the command callable but hides it from help. Use with When.
func Hide() Transform { return func(op *spec.Operation) { op.Hidden = true } }

// Example sets the example text shown in help. Use with When.
func Example(example string) Transform { return func(op *spec.Operation) { op.Example = example } }

// TrimGroupFromName removes words that repeat the group name, so
// "pet get-pet-by-id" becomes "pet get-by-id" and "pet find-pets-by-status"
// becomes "pet find-by-status".
func TrimGroupFromName() Transform {
	return func(op *spec.Operation) {
		if op.Group == "" {
			return
		}
		group := strings.Split(op.Group, "-")
		words := strings.Split(op.Name, "-")
		var out []string
		for i := 0; i < len(words); i++ {
			if i+len(group) <= len(words) && matchesGroup(words[i:i+len(group)], group) {
				i += len(group) - 1
				continue
			}
			out = append(out, words[i])
		}
		if len(out) > 0 {
			op.Name = strings.Join(out, "-")
		}
	}
}

func matchesGroup(words, group []string) bool {
	for i := range group {
		w, g := words[i], group[i]
		if i == len(group)-1 {
			w, g = strings.TrimSuffix(w, "s"), strings.TrimSuffix(g, "s")
		}
		if w != g {
			return false
		}
	}
	return true
}

// --- Descriptions -----------------------------------------------------------
//
// Help text and the agent tool catalog are only as good as the spec's
// summaries and descriptions. These fill gaps without editing the spec.

// Summary sets the one-line summary. Use with When.
func Summary(summary string) Transform { return func(op *spec.Operation) { op.Summary = summary } }

// Description sets the long description. Use with When.
func Description(description string) Transform {
	return func(op *spec.Operation) { op.Description = description }
}

// DescribeParam sets a parameter's description on every operation that has it.
func DescribeParam(param, description string) Transform {
	return Param(param, func(p *spec.Param) { p.Description = description })
}

// OperationDocs overrides documentation for one operation.
type OperationDocs struct {
	Summary     string            `json:"summary,omitempty"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"` // param (spec or flag name) -> description
}

// Docs overrides documentation in bulk. Keys are operation IDs or
// "METHOD /path"; empty fields leave the spec's text in place.
func Docs(docs map[string]OperationDocs) Transform {
	return func(op *spec.Operation) {
		d, ok := docs[op.ID]
		if !ok {
			if d, ok = docs[op.Key()]; !ok {
				return
			}
		}
		if d.Summary != "" {
			op.Summary = d.Summary
		}
		if d.Description != "" {
			op.Description = d.Description
		}
		for name, desc := range d.Params {
			if p := op.Param(name); p != nil {
				p.Description = desc
			}
		}
	}
}

// DocsFromYAML parses a YAML or JSON overlay for Docs:
//
//	listPets:
//	  summary: List pets in the store
//	  description: Results are paginated; use --limit to control page size.
//	  params:
//	    status: Only return pets in these states
//	"DELETE /pets/{petId}":
//	  summary: Permanently delete a pet
func DocsFromYAML(data []byte) (Transform, error) {
	var docs map[string]OperationDocs
	if err := yaml.Unmarshal(data, &docs); err != nil {
		return nil, fmt.Errorf("parsing docs overlay: %w", err)
	}
	return Docs(docs), nil
}

// --- Parameters -------------------------------------------------------------

// Param applies f to the named parameter (spec name or flag name) of every
// operation that has it.
func Param(name string, f func(p *spec.Param)) Transform {
	return func(op *spec.Operation) {
		if p := op.Param(name); p != nil {
			f(p)
		}
	}
}

// Preset sends value for a parameter when the user doesn't set the flag.
func Preset(param string, value any) Transform {
	return Param(param, func(p *spec.Param) { p.Preset = value })
}

// ParamFromEnv reads a parameter from an environment variable when the flag isn't set.
func ParamFromEnv(param, env string) Transform {
	return Param(param, func(p *spec.Param) { p.EnvVar = env })
}

// HideParam hides flags from help; they still work.
func HideParam(params ...string) Transform {
	return func(op *spec.Operation) {
		for _, name := range params {
			if p := op.Param(name); p != nil {
				p.Hidden = true
			}
		}
	}
}

// RemoveParam stops exposing parameters entirely. Pair required parameters
// with a request hook or Preset so the request still succeeds.
func RemoveParam(params ...string) Transform {
	return func(op *spec.Operation) {
		for _, name := range params {
			op.RemoveParam(name)
		}
	}
}

// RenameFlag renames a parameter's flag.
func RenameFlag(param, flag string) Transform {
	return Param(param, func(p *spec.Param) { p.Flag = flag })
}

// ShortFlag gives a parameter's flag a one-letter shorthand.
func ShortFlag(param, short string) Transform {
	return Param(param, func(p *spec.Param) { p.Short = short })
}

// PositionalPathParams turns path parameters into positional arguments:
// "pet get-by-id 42" instead of "pet get-by-id --pet-id 42".
func PositionalPathParams() Transform {
	return func(op *spec.Operation) {
		for _, p := range op.Params {
			if p.In == spec.InPath {
				p.Positional = true
			}
		}
	}
}

// --- Vendor extensions --------------------------------------------------------

// Vendor extensions recognized by VendorExtensions. They let API authors
// shape the CLI from the spec itself.
const (
	ExtIgnore  = "x-cli-ignore"  // operation or param: don't expose
	ExtName    = "x-cli-name"    // operation: command name; param: flag name
	ExtGroup   = "x-cli-group"   // operation: group name
	ExtAliases = "x-cli-aliases" // operation: list of aliases
	ExtHidden  = "x-cli-hidden"  // operation or param: hide from help
	ExtEnv     = "x-cli-env"     // param: environment variable fallback
	ExtShort   = "x-cli-short"   // param: flag shorthand
	// Documentation overrides, for when the public description isn't what a
	// CLI user or agent needs.
	ExtSummary     = "x-cli-summary"     // operation: one-line summary
	ExtDescription = "x-cli-description" // operation or param: description
)

// VendorExtensions applies the x-cli-* extensions above. forge enables it by
// default; x-cli-ignore on operations is handled by filter.ExcludeExtension.
func VendorExtensions() Transform {
	return func(op *spec.Operation) {
		if v, ok := stringExt(op.Extensions, ExtName); ok {
			op.Name = v
		}
		if v, ok := stringExt(op.Extensions, ExtGroup); ok {
			op.Group = v
		}
		if v, ok := op.Extensions[ExtAliases]; ok {
			if list, ok := v.([]any); ok {
				for _, a := range list {
					op.Aliases = append(op.Aliases, fmt.Sprint(a))
				}
			}
		}
		if v, ok := op.Extensions[ExtHidden]; ok && filter.Truthy(v) {
			op.Hidden = true
		}
		if v, ok := stringExt(op.Extensions, ExtSummary); ok {
			op.Summary = v
		}
		if v, ok := stringExt(op.Extensions, ExtDescription); ok {
			op.Description = v
		}
		for _, p := range op.AllParams() {
			if v, ok := p.Extensions[ExtIgnore]; ok && filter.Truthy(v) {
				op.RemoveParam(p.Name)
				continue
			}
			if v, ok := stringExt(p.Extensions, ExtName); ok {
				p.Flag = v
			}
			if v, ok := stringExt(p.Extensions, ExtEnv); ok {
				p.EnvVar = v
			}
			if v, ok := stringExt(p.Extensions, ExtShort); ok {
				p.Short = v
			}
			if v, ok := p.Extensions[ExtHidden]; ok && filter.Truthy(v) {
				p.Hidden = true
			}
			if v, ok := stringExt(p.Extensions, ExtDescription); ok {
				p.Description = v
			}
		}
	}
}

func stringExt(ext map[string]any, key string) (string, bool) {
	v, ok := ext[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

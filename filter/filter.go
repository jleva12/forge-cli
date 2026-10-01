// Package filter decides which API operations are exposed as commands.
//
// A Matcher is a predicate over operations. A Filter wraps matchers with
// include/exclude semantics. An operation is exposed only if every
// configured Filter keeps it:
//
//	forge.WithFilters(
//		filter.ExcludeTags("internal"),
//		filter.Exclude(filter.Path("/admin/**"), filter.Method("DELETE")),
//		filter.ExcludeDeprecated(),
//	)
package filter

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	
	"forge-cli/spec"
)

// Matcher reports whether an operation matches.
type Matcher func(op *spec.Operation) bool

// Filter reports whether an operation should be kept.
type Filter func(op *spec.Operation) bool

// Include keeps only operations matching at least one matcher.
func Include(ms ...Matcher) Filter { return Filter(Any(ms...)) }

// Exclude drops operations matching any matcher.
func Exclude(ms ...Matcher) Filter { return Filter(Not(Any(ms...))) }

// --- Common filters -------------------------------------------------------

func IncludeTags(tags ...string) Filter      { return Include(Tag(tags...)) }
func ExcludeTags(tags ...string) Filter      { return Exclude(Tag(tags...)) }
func IncludePaths(globs ...string) Filter    { return Include(Path(globs...)) }
func ExcludePaths(globs ...string) Filter    { return Exclude(Path(globs...)) }
func IncludeOperations(ids ...string) Filter { return Include(OperationID(ids...)) }
func ExcludeOperations(ids ...string) Filter { return Exclude(OperationID(ids...)) }
func ExcludeMethods(methods ...string) Filter {
	return Exclude(Method(methods...))
}
func ExcludeDeprecated() Filter { return Exclude(Deprecated()) }

// ReadOnly keeps only GET, HEAD and OPTIONS operations.
func ReadOnly() Filter { return Include(Method("GET", "HEAD", "OPTIONS")) }

// ExcludeExtension drops operations whose vendor extension is set and truthy,
// e.g. ExcludeExtension("x-internal").
func ExcludeExtension(keys ...string) Filter {
	ms := make([]Matcher, len(keys))
	for i, k := range keys {
		ms[i] = Extension(k)
	}
	return Exclude(ms...)
}

// --- Matchers --------------------------------------------------------------

// Tag matches operations carrying any of the tags (case-insensitive).
func Tag(tags ...string) Matcher {
	return func(op *spec.Operation) bool {
		for _, t := range op.Tags {
			for _, want := range tags {
				if strings.EqualFold(t, want) {
					return true
				}
			}
		}
		return false
	}
}

// Path matches the spec path against globs. "*" matches within one segment,
// "**" matches across segments, and a trailing "/**" also matches the prefix
// itself. Parameters are matched literally: "/pets/{petId}".
func Path(globs ...string) Matcher {
	res := make([]*regexp.Regexp, len(globs))
	for i, g := range globs {
		res[i] = Glob(g)
	}
	return func(op *spec.Operation) bool {
		for _, re := range res {
			if re.MatchString(op.Path) {
				return true
			}
		}
		return false
	}
}

// PathRegexp matches the spec path against a regular expression.
func PathRegexp(expr string) Matcher {
	re := regexp.MustCompile(expr)
	return func(op *spec.Operation) bool { return re.MatchString(op.Path) }
}

// Method matches HTTP methods (case-insensitive).
func Method(methods ...string) Matcher {
	return func(op *spec.Operation) bool {
		for _, m := range methods {
			if strings.EqualFold(op.Method, m) {
				return true
			}
		}
		return false
	}
}

// Route matches "METHOD /path/glob" strings; the method may be "*".
// Example: Route("DELETE /**", "* /admin/**", "GET /pets/{petId}").
func Route(routes ...string) Matcher {
	ms := make([]Matcher, len(routes))
	for i, r := range routes {
		method, path, ok := strings.Cut(strings.TrimSpace(r), " ")
		if !ok {
			panic(fmt.Sprintf("filter.Route: %q must look like \"METHOD /path\"", r))
		}
		pm := Path(strings.TrimSpace(path))
		if method == "*" {
			ms[i] = pm
		} else {
			ms[i] = All(Method(method), pm)
		}
	}
	return Any(ms...)
}

// OperationID matches operation IDs exactly.
func OperationID(ids ...string) Matcher {
	return func(op *spec.Operation) bool {
		return slices.Contains(ids, op.ID)
	}
}

// Group matches the generated command group (after naming).
func Group(groups ...string) Matcher {
	return func(op *spec.Operation) bool {
		return slices.Contains(groups, op.Group)
	}
}

// Deprecated matches operations marked deprecated.
func Deprecated() Matcher { return func(op *spec.Operation) bool { return op.Deprecated } }

// Extension matches operations whose vendor extension is present and truthy
// (anything other than false, "false", "", 0 or null).
func Extension(key string) Matcher {
	return func(op *spec.Operation) bool {
		v, ok := op.Extension(key)
		return ok && Truthy(v)
	}
}

// Func adapts an arbitrary predicate.
func Func(f func(op *spec.Operation) bool) Matcher { return f }

// All matches when every matcher matches.
func All(ms ...Matcher) Matcher {
	return func(op *spec.Operation) bool {
		for _, m := range ms {
			if !m(op) {
				return false
			}
		}
		return true
	}
}

// Any matches when at least one matcher matches.
func Any(ms ...Matcher) Matcher {
	return func(op *spec.Operation) bool {
		for _, m := range ms {
			if m(op) {
				return true
			}
		}
		return false
	}
}

// Not inverts a matcher.
func Not(m Matcher) Matcher { return func(op *spec.Operation) bool { return !m(op) } }

// Glob compiles a path glob into an anchored regular expression.
func Glob(pattern string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	p := pattern
	suffix := ""
	if strings.HasSuffix(p, "/**") {
		p, suffix = strings.TrimSuffix(p, "/**"), "(/.*)?"
	}
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case p[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
		}
	}
	b.WriteString(suffix + "$")
	return regexp.MustCompile(b.String())
}

// Truthy interprets a vendor extension value as a boolean.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && !strings.EqualFold(x, "false") && x != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return true
}

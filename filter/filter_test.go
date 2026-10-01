package filter

import (
	"testing"

	"forge-cli/spec"
)

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/pets", "/pets", true},
		{"/pets/*", "/pets/{petId}", true},
		{"/pets/*", "/pets/{petId}/photo", false},
		{"/pets/**", "/pets/{petId}/photo", true},
		{"/pets/**", "/pets", true},
		{"/admin/**", "/administrators", false},
		{"/**/photo", "/pets/{petId}/photo", true},
		{"/pets/{petId}", "/pets/{petId}", true},
	}
	for _, c := range cases {
		if got := Glob(c.pattern).MatchString(c.path); got != c.want {
			t.Errorf("Glob(%q).Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestFilters(t *testing.T) {
	get := &spec.Operation{Method: "GET", Path: "/pets/{id}", Tags: []string{"Pet"}, ID: "getPet"}
	del := &spec.Operation{Method: "DELETE", Path: "/pets/{id}", Tags: []string{"pet"}, Deprecated: true,
		Extensions: map[string]any{"x-internal": true}}

	check := func(name string, f Filter, wantGet, wantDel bool) {
		t.Helper()
		if f(get) != wantGet || f(del) != wantDel {
			t.Errorf("%s: got (%v,%v), want (%v,%v)", name, f(get), f(del), wantGet, wantDel)
		}
	}
	check("ExcludeTags case-insensitive", ExcludeTags("pet"), false, false)
	check("Route", Exclude(Route("DELETE /pets/*")), true, false)
	check("Route wildcard method", Exclude(Route("* /pets/**")), false, false)
	check("ReadOnly", ReadOnly(), true, false)
	check("ExcludeDeprecated", ExcludeDeprecated(), true, false)
	check("ExcludeExtension", ExcludeExtension("x-internal"), true, false)
	check("IncludeOperations", IncludeOperations("getPet"), true, false)
	check("All/Not", Filter(All(Tag("pet"), Not(Method("GET")))), false, true)
}

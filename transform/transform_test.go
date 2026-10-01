package transform

import (
	"testing"

	"forge-cli/spec"
)

func TestTrimGroupFromName(t *testing.T) {
	for _, c := range []struct{ group, name, want string }{
		{"pet", "get-pet-by-id", "get-by-id"},
		{"pet", "list-pets", "list"},
		{"pet", "find-pets-by-status", "find-by-status"},
		{"pet", "pet", "pet"},
		{"store", "get-inventory", "get-inventory"},
		{"user-group", "list-user-groups", "list"},
		{"", "list-pets", "list-pets"},
	} {
		op := &spec.Operation{Group: c.group, Name: c.name}
		TrimGroupFromName()(op)
		if op.Name != c.want {
			t.Errorf("group %q name %q -> %q, want %q", c.group, c.name, op.Name, c.want)
		}
	}
}

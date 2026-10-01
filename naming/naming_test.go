package naming

import (
	"testing"

	"github.com/jleva12/forge-cli/spec"
)

func TestKebab(t *testing.T) {
	for in, want := range map[string]string{
		"getPetById":     "get-pet-by-id",
		"getPetByID":     "get-pet-by-id",
		"HTTPServer":     "http-server",
		"user_login":     "user-login",
		"X-Request-ID":   "x-request-id",
		"Pet Store":      "pet-store",
		"v2Items":        "v2-items",
		"already-kebab":  "already-kebab",
		"owner.name":     "owner-name",
		"  __weird__  ":  "weird",
		"listPets2Fast":  "list-pets2-fast",
		"OAuthCallbacks": "o-auth-callbacks",
	} {
		if got := Kebab(in); got != want {
			t.Errorf("Kebab(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultNamer(t *testing.T) {
	d := Default{}
	op := &spec.Operation{Method: "GET", Path: "/api/v1/pets/{petId}/photos"}
	if got := d.Command(op); got != "get-pets-photos-by-pet-id" {
		t.Errorf("Command = %q", got)
	}
	if got := d.Group(op); got != "pets" {
		t.Errorf("Group = %q", got)
	}
	op.Tags = []string{"PetPhotos"}
	if got := d.Group(op); got != "pet-photos" {
		t.Errorf("Group with tag = %q", got)
	}
	p := &spec.Param{Name: "owner.firstName", BodyPath: []string{"owner", "firstName"}}
	if got := d.Flag(op, p); got != "owner.first-name" {
		t.Errorf("Flag = %q", got)
	}
}

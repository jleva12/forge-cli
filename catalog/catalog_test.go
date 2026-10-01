package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/jleva12/forge-cli/spec"
)

func TestSchemaInlinesRefsAndStopsRecursion(t *testing.T) {
	node := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{}}
	node.Properties["name"] = &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}}
	node.Properties["secret"] = &openapi3.SchemaRef{Value: &openapi3.Schema{Type: &openapi3.Types{"string"}, WriteOnly: true}}
	node.Properties["children"] = &openapi3.SchemaRef{Value: &openapi3.Schema{
		Type: &openapi3.Types{"array"}, Items: &openapi3.SchemaRef{Ref: "#/components/schemas/Node", Value: node},
	}}

	out := Schema(node, 10, false)
	b, _ := json.Marshal(out)
	got := string(b)
	if strings.Contains(got, "$ref") {
		t.Errorf("schema still has $ref: %s", got)
	}
	if !strings.Contains(got, "recursive") {
		t.Errorf("recursion not cut off: %s", got)
	}
	if strings.Contains(got, "secret") {
		t.Errorf("writeOnly property in output schema: %s", got)
	}
}

func TestSchemaDepthKeepsScalars(t *testing.T) {
	inner := &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
		"leaf": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
		"deep": {Value: &openapi3.Schema{Type: &openapi3.Types{"object"}, Properties: openapi3.Schemas{
			"x": {Value: &openapi3.Schema{Type: &openapi3.Types{"string"}}},
		}}},
	}}
	out := Schema(inner, 1, false)
	props := out["properties"].(map[string]any)
	if _, ok := props["leaf"].(map[string]any)["description"]; ok {
		t.Error("scalar leaf should not be marked as omitted")
	}
	if d, _ := props["deep"].(map[string]any)["description"].(string); !strings.Contains(d, "omitted") {
		t.Errorf("deep object should be omitted, got %v", props["deep"])
	}
}

func TestInputSchemaAndNames(t *testing.T) {
	op := &spec.Operation{
		Group: "pet-store", Name: "create.pet", Method: "POST", Path: "/pets",
		Summary: "Create a pet", Description: "<p>Adds a <b>pet</b>.</p>",
		Params: []*spec.Param{
			{Name: "X-Org", Flag: "x-org", In: spec.InHeader, Type: spec.TypeString, Required: true, EnvVar: "ORG"},
			{Name: "dryRun", Flag: "dry", In: spec.InQuery, Type: spec.TypeBoolean, Hidden: true},
		},
		Body: &spec.Body{Required: true, ContentType: "application/json", Fields: []*spec.Param{
			{Name: "name", Flag: "name", In: spec.InBody, BodyPath: []string{"name"}, Type: spec.TypeString, Required: true},
			{Name: "age", Flag: "age", In: spec.InBody, BodyPath: []string{"age"}, Type: spec.TypeInteger, Enum: []string{"1", "2"}},
		}},
		Security: []spec.SecurityRequirement{{"key": nil}, {}},
	}
	tools := Build([]*spec.Operation{op}, Options{CLIName: "cli"})
	if len(tools) != 1 {
		t.Fatalf("got %d tools", len(tools))
	}
	tool := tools[0]
	if tool.Name != "pet_store_create_pet" {
		t.Errorf("name = %q", tool.Name)
	}
	if tool.Description != "Create a pet.\n\nAdds a pet." {
		t.Errorf("description = %q", tool.Description)
	}
	props := tool.InputSchema["properties"].(map[string]any)
	if _, ok := props["dry"]; ok {
		t.Error("hidden param exposed")
	}
	if got := props["age"].(map[string]any)["enum"].([]any)[0]; got != int64(1) {
		t.Errorf("integer enum not typed: %#v", got)
	}
	// x-org has an env fallback, so it isn't required; name is (body is required).
	if req := tool.InputSchema["required"].([]string); len(req) != 1 || req[0] != "name" {
		t.Errorf("required = %v", req)
	}
	if len(tool.Auth) != 1 || tool.Auth[0][0] != "key" {
		t.Errorf("auth = %v", tool.Auth)
	}
}

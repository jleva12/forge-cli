// Package spec loads OpenAPI documents and normalizes them into a small,
// CLI-oriented model. Everything downstream (naming, filtering, transforms,
// command building, request building) works on this model rather than on the
// raw OpenAPI types, so the parser can be swapped without touching the rest.
package spec

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Location is where a parameter is sent in the HTTP request.
type Location string

const (
	InPath   Location = "path"
	InQuery  Location = "query"
	InHeader Location = "header"
	InCookie Location = "cookie"
	InBody   Location = "body"
)

// Type is the simplified value type of a parameter or body field.
type Type string

const (
	TypeString  Type = "string"
	TypeInteger Type = "integer"
	TypeNumber  Type = "number"
	TypeBoolean Type = "boolean"
	TypeArray   Type = "array"
	// TypeObject values are passed on the command line as JSON strings.
	TypeObject Type = "object"
)

// API is a normalized OpenAPI document.
type API struct {
	Title       string
	Version     string
	Description string
	Servers     []Server
	// Tags maps tag name to its description.
	Tags            map[string]string
	SecuritySchemes map[string]*SecurityScheme
	Operations      []*Operation
	// Source is the file path or URL the document was loaded from, if any.
	Source string
	// Doc is the underlying parsed document, for anything the model doesn't cover.
	Doc *openapi3.T
}

// Server is an API server with its variables already substituted by their defaults.
type Server struct {
	URL         string
	Description string
}

// SecurityScheme describes one entry of components.securitySchemes.
type SecurityScheme struct {
	Name         string
	Type         string // apiKey, http, oauth2, openIdConnect, mutualTLS
	Scheme       string // for http: basic, bearer, ...
	BearerFormat string
	// In and ParamName apply to apiKey schemes.
	In               Location
	ParamName        string
	Description      string
	Flows            *openapi3.OAuthFlows
	OpenIDConnectURL string
}

// SecurityRequirement maps scheme names to required scopes. An operation lists
// alternatives: satisfying any one requirement is enough.
type SecurityRequirement map[string][]string

// Operation is a single HTTP operation plus its CLI presentation.
type Operation struct {
	ID          string
	Method      string // upper case, e.g. GET
	Path        string // as written in the spec, e.g. /pets/{petId}
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool
	Params      []*Param
	Body        *Body
	// Responses are sorted by status code, with "default" last.
	Responses []*Response
	// Security is the effective security (operation-level, else global).
	Security   []SecurityRequirement
	Extensions map[string]any

	// CLI presentation. Populated by a naming.Namer and adjustable by transforms.
	Group   string // parent command; empty means the root command
	Name    string
	Aliases []string
	Hidden  bool
	Example string

	// Raw is the underlying parsed operation.
	Raw *openapi3.Operation
}

// Key returns "METHOD /path", which uniquely identifies an operation.
func (o *Operation) Key() string { return o.Method + " " + o.Path }

// CommandPath returns the command as typed after the binary name, e.g. "pet get".
func (o *Operation) CommandPath() string {
	return strings.TrimSpace(o.Group + " " + o.Name)
}

// Extension returns a vendor extension value (e.g. "x-internal").
func (o *Operation) Extension(key string) (any, bool) {
	v, ok := o.Extensions[key]
	return v, ok
}

// AllParams returns the parameters followed by the body fields.
func (o *Operation) AllParams() []*Param {
	out := append([]*Param(nil), o.Params...)
	if o.Body != nil {
		out = append(out, o.Body.Fields...)
	}
	return out
}

// Param finds a parameter or body field by its spec name or its flag name.
func (o *Operation) Param(name string) *Param {
	for _, p := range o.AllParams() {
		if p.Name == name || p.Flag == name {
			return p
		}
	}
	return nil
}

// RemoveParam drops a parameter or body field so it is no longer exposed.
func (o *Operation) RemoveParam(name string) {
	keep := func(ps []*Param) []*Param {
		out := ps[:0]
		for _, p := range ps {
			if p.Name != name && p.Flag != name {
				out = append(out, p)
			}
		}
		return out
	}
	o.Params = keep(o.Params)
	if o.Body != nil {
		o.Body.Fields = keep(o.Body.Fields)
	}
}

// Param is a path/query/header/cookie parameter or a flattened body field.
type Param struct {
	// Name is the name used on the wire. For body fields it is the dotted
	// property path, e.g. "owner.name".
	Name string
	In   Location
	// BodyPath is the property path for body fields.
	BodyPath    []string
	Description string
	Required    bool
	Deprecated  bool
	Type        Type
	ItemType    Type // for TypeArray
	Format      string
	Enum        []string
	// Default is the spec's default. It is shown in help but never sent;
	// the server applies it.
	Default    any
	Extensions map[string]any
	// Schema is the underlying schema, used e.g. to describe JSON-valued fields.
	Schema *openapi3.Schema

	// CLI presentation.
	Flag  string
	Short string
	// Positional makes the parameter a positional argument instead of a flag.
	Positional bool
	Hidden     bool
	// Preset is sent when the user doesn't set the flag.
	Preset any
	// EnvVar is read when the user doesn't set the flag.
	EnvVar string
}

func (p *Param) String() string { return fmt.Sprintf("%s(%s)", p.Name, p.In) }

// Body describes an operation's request body.
type Body struct {
	ContentType string
	// ContentTypes lists every media type the operation accepts.
	ContentTypes []string
	Required     bool
	Description  string
	// Fields are the body properties exposed as flags. Anything not covered
	// can still be sent with --body.
	Fields []*Param
	Schema *openapi3.Schema
}

// Response describes one documented response of an operation.
type Response struct {
	Status      string // "200", "4XX", "default", ...
	Description string
	ContentType string
	Schema      *openapi3.Schema
}

// Success returns the first documented 2xx response, if any.
func (o *Operation) Success() *Response {
	for _, r := range o.Responses {
		if strings.HasPrefix(r.Status, "2") {
			return r
		}
	}
	return nil
}

package spec

import (
	"context"
	"net/url"
	"testing"
)

func TestServersDefaultToSpecLocation(t *testing.T) {
	for _, tc := range []struct{ name, doc, base, want string }{
		{
			"openapi 3 without servers",
			`{"openapi": "3.1.0", "info": {"title": "t", "version": "1"}, "paths": {}}`,
			"http://127.0.0.1:8000/openapi.json", "http://127.0.0.1:8000/",
		},
		{
			"swagger 2 without host",
			`{"swagger": "2.0", "info": {"title": "t", "version": "1"}, "basePath": "/api/v1", "paths": {}}`,
			"https://codeberg.org/swagger.v1.json", "https://codeberg.org/api/v1",
		},
		{
			"absolute servers are kept",
			`{"openapi": "3.0.3", "info": {"title": "t", "version": "1"}, "servers": [{"url": "https://api.example.com/v2"}], "paths": {}}`,
			"http://127.0.0.1:8000/openapi.json", "https://api.example.com/v2",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := url.Parse(tc.base)
			api, err := LoadData(context.Background(), []byte(tc.doc), base, LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(api.Servers) != 1 || api.Servers[0].URL != tc.want {
				t.Errorf("servers = %+v, want %s", api.Servers, tc.want)
			}
		})
	}
}

func TestParseJSONThatYAMLRejects(t *testing.T) {
	doc := "\xef\xbb\xbf" + `{"openapi": "3.0.3", "info": {"title": "thumbs 👍", "version": "1"}, "paths": {}}`
	api, err := LoadData(context.Background(), []byte(doc), nil, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if api.Title != "thumbs 👍" {
		t.Errorf("title = %q", api.Title)
	}
}

func TestOpenAPI31FileFields(t *testing.T) {
	doc := `{"openapi": "3.1.0", "info": {"title": "t", "version": "1"}, "paths": {
  "/upload": {"post": {"requestBody": {"content": {"multipart/form-data": {"schema": {"type": "object", "properties": {
    "file": {"type": "string", "contentMediaType": "application/octet-stream"},
    "encoded": {"type": "string", "contentMediaType": "image/png", "contentEncoding": "base64"},
    "name": {"type": "string"}}}}}}, "responses": {"200": {"description": "ok"}}}},
  "/page": {"post": {"requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {
    "html": {"type": "string", "contentMediaType": "text/html"}}}}}}, "responses": {"200": {"description": "ok"}}}}}}`
	api, err := LoadData(context.Background(), []byte(doc), nil, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, op := range api.Operations {
		for _, f := range op.Body.Fields {
			got[op.Path+" "+f.Name] = f.Format
		}
	}
	// In a JSON body, contentMediaType describes text, not a file.
	want := map[string]string{"/upload file": "binary", "/upload encoded": "byte", "/upload name": "", "/page html": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: format = %q, want %q", k, got[k], v)
		}
	}
}

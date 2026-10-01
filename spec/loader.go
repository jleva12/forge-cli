package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"sigs.k8s.io/yaml"
)

// LoadOptions controls loading and normalization.
type LoadOptions struct {
	// HTTPClient fetches specs from URLs. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Validate runs full OpenAPI validation. Off by default because many
	// real-world specs are slightly invalid but still perfectly usable.
	Validate  bool
	Normalize NormalizeOptions
}

// Load reads a spec from a file path or an http(s) URL. JSON and YAML are
// both accepted, as are OpenAPI 3.x and Swagger 2.0 documents.
func Load(ctx context.Context, location string, opts LoadOptions) (*API, error) {
	var (
		data []byte
		base *url.URL
		err  error
	)
	if isURL(location) {
		base, err = url.Parse(location)
		if err != nil {
			return nil, err
		}
		data, err = fetch(ctx, opts.HTTPClient, location)
	} else {
		var abs string
		if abs, err = filepath.Abs(location); err == nil {
			base = &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
			data, err = os.ReadFile(location)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("reading spec %s: %w", location, err)
	}
	api, err := LoadData(ctx, data, base, opts)
	if err != nil {
		return nil, err
	}
	api.Source = location
	return api, nil
}

// LoadData parses spec bytes (e.g. from go:embed). base, if set, is used to
// resolve relative $refs and relative server URLs.
func LoadData(ctx context.Context, data []byte, base *url.URL, opts LoadOptions) (*API, error) {
	doc, err := Parse(data, base)
	if err != nil {
		return nil, err
	}
	if opts.Validate {
		if err := doc.Validate(ctx); err != nil {
			return nil, fmt.Errorf("invalid spec: %w", err)
		}
	}
	api, err := Normalize(doc, opts.Normalize)
	if err != nil {
		return nil, err
	}
	if base != nil && (base.Scheme == "http" || base.Scheme == "https") {
		for i, s := range api.Servers {
			if u, err := url.Parse(s.URL); err == nil && !u.IsAbs() {
				api.Servers[i].URL = base.ResolveReference(u).String()
			}
		}
	}
	return api, nil
}

// Parse parses spec bytes into an OpenAPI 3 document, converting Swagger 2.0 if needed.
//
// A document without servers gets the default the standards define: "/" for
// OpenAPI 3, and the basePath for Swagger 2.0 without a host. Both are
// relative to wherever the spec was loaded from, and LoadData resolves them.
func Parse(data []byte, base *url.URL) (*openapi3.T, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	// JSON is parsed as is: the YAML parser rejects some valid JSON, such as
	// escaped emoji ("👍").
	jsonData := data
	if !json.Valid(data) {
		var err error
		if jsonData, err = yaml.YAMLToJSON(data); err != nil {
			return nil, fmt.Errorf("spec is neither JSON nor YAML: %w", err)
		}
	}
	var probe struct {
		Swagger string `json:"swagger"`
		OpenAPI string `json:"openapi"`
	}
	if err := json.Unmarshal(jsonData, &probe); err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	switch {
	case strings.HasPrefix(probe.Swagger, "2"):
		var doc2 openapi2.T
		if err := json.Unmarshal(jsonData, &doc2); err != nil {
			return nil, fmt.Errorf("parsing swagger 2.0 spec: %w", err)
		}
		doc, err := openapi2conv.ToV3(&doc2)
		if err != nil {
			return nil, fmt.Errorf("converting swagger 2.0 spec: %w", err)
		}
		if doc2.Host == "" {
			// The converter drops basePath when there's no host.
			doc.AddServer(&openapi3.Server{URL: "/" + strings.TrimPrefix(doc2.BasePath, "/")})
		}
		return doc, nil
	case probe.OpenAPI == "":
		return nil, fmt.Errorf("not an OpenAPI document: missing \"openapi\" or \"swagger\" version field")
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	var (
		doc *openapi3.T
		err error
	)
	if base != nil {
		doc, err = loader.LoadFromDataWithPath(jsonData, base)
	} else {
		doc, err = loader.LoadFromData(jsonData)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing spec: %w", err)
	}
	if len(doc.Servers) == 0 {
		doc.AddServer(&openapi3.Server{URL: "/"})
	}
	return doc, nil
}

func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func fetch(ctx context.Context, client *http.Client, location string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/yaml;q=0.9, */*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// Package request turns an operation plus user-supplied values into an *http.Request.
package request

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/transport"
)

// Input holds the values for one invocation.
type Input struct {
	// Values are typed parameter/body-field values keyed by the param.
	// Only parameters the user (or a preset/env var) supplied are present.
	Values map[*spec.Param]any
	// RawBody is a complete body from --body. Body-field values are merged
	// on top of it for JSON bodies.
	RawBody []byte
	// ContentType overrides the operation's preferred body content type.
	ContentType string
	// Headers are extra headers (e.g. from -H).
	Headers http.Header
}

// Build creates the HTTP request for op against baseURL.
func Build(ctx context.Context, baseURL string, op *spec.Operation, in Input) (*http.Request, error) {
	path := op.Path
	query := url.Values{}
	headers := http.Header{}
	var cookies []*http.Cookie

	for _, p := range op.Params {
		v, ok := in.Values[p]
		if !ok {
			if p.Required && p.In == spec.InPath {
				return nil, fmt.Errorf("missing required path parameter %q", p.Name)
			}
			continue
		}
		switch p.In {
		case spec.InPath:
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(Stringify(v)))
		case spec.InQuery:
			for _, s := range stringsOf(v) {
				query.Add(p.Name, s)
			}
		case spec.InHeader:
			headers.Set(p.Name, strings.Join(stringsOf(v), ","))
		case spec.InCookie:
			cookies = append(cookies, &http.Cookie{Name: p.Name, Value: Stringify(v)})
		}
	}

	u, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}
	if len(query) > 0 {
		q := u.Query()
		for k, vs := range query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}

	body, contentType, err := buildBody(op, in)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, */*;q=0.8")
	for k, vs := range headers {
		req.Header[k] = vs
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, vs := range in.Headers {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

func buildBody(op *spec.Operation, in Input) ([]byte, string, error) {
	var fields []*spec.Param
	if op.Body != nil {
		for _, p := range op.Body.Fields {
			if _, ok := in.Values[p]; ok {
				fields = append(fields, p)
			}
		}
	}
	if in.RawBody == nil && len(fields) == 0 {
		return nil, "", nil
	}
	ct := in.ContentType
	if ct == "" && op.Body != nil {
		ct = op.Body.ContentType
	}
	if ct == "" {
		ct = "application/json"
	}
	if len(fields) == 0 {
		return in.RawBody, ct, nil
	}

	switch {
	case strings.Contains(ct, "json"):
		obj := map[string]any{}
		if len(bytes.TrimSpace(in.RawBody)) > 0 {
			if err := json.Unmarshal(in.RawBody, &obj); err != nil {
				return nil, "", fmt.Errorf("--body must be a JSON object to combine it with field flags: %w", err)
			}
		}
		for _, p := range fields {
			setPath(obj, p.BodyPath, in.Values[p])
		}
		b, err := json.Marshal(obj)
		return b, ct, err

	case ct == "application/x-www-form-urlencoded":
		form, err := url.ParseQuery(string(in.RawBody))
		if err != nil {
			return nil, "", fmt.Errorf("parsing --body as form data: %w", err)
		}
		for _, p := range fields {
			form.Del(p.Name)
			for _, s := range stringsOf(in.Values[p]) {
				form.Add(p.Name, s)
			}
		}
		return []byte(form.Encode()), ct, nil

	case strings.HasPrefix(ct, "multipart/form-data"):
		return buildMultipart(fields, in.Values)
	}
	return nil, "", fmt.Errorf("content type %s only supports --body, not field flags", ct)
}

// buildMultipart sends format=binary fields as file uploads (the flag value
// is a path, optionally prefixed with @) and everything else as form fields.
func buildMultipart(fields []*spec.Param, values map[*spec.Param]any) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range fields {
		if p.Format == "binary" || p.Format == "byte" {
			path := strings.TrimPrefix(Stringify(values[p]), "@")
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, "", fmt.Errorf("--%s: %w", p.Flag, err)
			}
			part, err := w.CreateFormFile(p.Name, filepath.Base(path))
			if err != nil {
				return nil, "", err
			}
			part.Write(data)
			continue
		}
		for _, s := range stringsOf(values[p]) {
			w.WriteField(p.Name, s)
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func setPath(obj map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := obj[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			obj[k] = next
		}
		obj = next
	}
	obj[path[len(path)-1]] = v
}

// Stringify renders a value for a URL, header or form field.
func Stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any, map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = Stringify(e)
		}
		return out
	case []string:
		return x
	}
	return []string{Stringify(v)}
}

// Curl renders req as a copy-pasteable curl command. Sensitive headers are
// redacted unless showSecrets is set.
func Curl(req *http.Request, showSecrets bool) string {
	if !showSecrets {
		req = transport.Redact(req)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X %s %s", req.Method, shellQuote(req.URL.String()))
	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range req.Header[k] {
			fmt.Fprintf(&b, " \\\n  -H %s", shellQuote(k+": "+v))
		}
	}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			data, _ := io.ReadAll(body)
			if len(data) > 0 {
				fmt.Fprintf(&b, " \\\n  --data-raw %s", shellQuote(string(data)))
			}
		}
	}
	return b.String()
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

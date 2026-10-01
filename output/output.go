// Package output renders API responses. Register custom formatters (tables,
// templates, ...) with forge.WithFormatter.
package output

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/jleva12/forge-cli/style"
)

// Response is a fully read HTTP response.
type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

// IsJSON reports whether the response body is JSON.
func (r *Response) IsJSON() bool {
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "json") {
		return true
	}
	return ct == "" && json.Valid(r.Body)
}

// Formatter writes a response body.
type Formatter interface {
	Format(w io.Writer, resp *Response) error
}

// FormatterFunc adapts a function to Formatter.
type FormatterFunc func(w io.Writer, resp *Response) error

func (f FormatterFunc) Format(w io.Writer, resp *Response) error { return f(w, resp) }

// JSON pretty-prints JSON responses and passes anything else through.
func JSON() Formatter {
	return FormatterFunc(func(w io.Writer, resp *Response) error {
		if !resp.IsJSON() {
			return Raw().Format(w, resp)
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, resp.Body, "", "  "); err != nil {
			return Raw().Format(w, resp)
		}
		buf.WriteByte('\n')
		_, err := w.Write(style.For(w).JSON(buf.Bytes()))
		return err
	})
}

// YAML converts JSON responses to YAML and passes anything else through.
func YAML() Formatter {
	return FormatterFunc(func(w io.Writer, resp *Response) error {
		if !resp.IsJSON() {
			return Raw().Format(w, resp)
		}
		out, err := yaml.JSONToYAML(resp.Body)
		if err != nil {
			return Raw().Format(w, resp)
		}
		_, err = w.Write(style.For(w).YAML(out))
		return err
	})
}

// Raw writes the body unchanged.
func Raw() Formatter {
	return FormatterFunc(func(w io.Writer, resp *Response) error {
		if len(resp.Body) == 0 {
			return nil
		}
		_, err := w.Write(resp.Body)
		if err == nil && !bytes.HasSuffix(resp.Body, []byte("\n")) {
			_, err = w.Write([]byte("\n"))
		}
		return err
	})
}

// Defaults returns the built-in formatters by name.
func Defaults() map[string]Formatter {
	return map[string]Formatter{"json": JSON(), "yaml": YAML(), "raw": Raw()}
}

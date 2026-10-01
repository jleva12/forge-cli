// Package transport provides composable HTTP middleware (http.RoundTripper
// wrappers) for the generated CLI: user agent, static headers, retries and
// request/response debugging.
package transport

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/jleva12/forge-cli/style"
)

// Middleware wraps a RoundTripper.
type Middleware func(next http.RoundTripper) http.RoundTripper

// RoundTripperFunc adapts a function to http.RoundTripper.
type RoundTripperFunc func(*http.Request) (*http.Response, error)

func (f RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Chain wraps base with middleware; the first middleware is outermost.
func Chain(base http.RoundTripper, mws ...Middleware) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	for i := len(mws) - 1; i >= 0; i-- {
		base = mws[i](base)
	}
	return base
}

// UserAgent sets the User-Agent header.
func UserAgent(ua string) Middleware { return Header("User-Agent", ua) }

// Header sets a header on every request unless it's already set.
func Header(key, value string) Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get(key) == "" {
				r = r.Clone(r.Context())
				r.Header.Set(key, value)
			}
			return next.RoundTrip(r)
		})
	}
}

// Retry retries idempotent requests on network errors and on 429/502/503/504,
// with exponential backoff starting at base and honoring Retry-After.
func Retry(maxRetries int, base time.Duration) Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			idempotent := r.Method == http.MethodGet || r.Method == http.MethodHead ||
				r.Method == http.MethodOptions || r.Method == http.MethodPut || r.Method == http.MethodDelete
			for attempt := 0; ; attempt++ {
				req := r
				if attempt > 0 && r.GetBody != nil {
					body, err := r.GetBody()
					if err != nil {
						return nil, err
					}
					req = r.Clone(r.Context())
					req.Body = body
				}
				resp, err := next.RoundTrip(req)
				if !idempotent || attempt >= maxRetries || !retryable(resp, err) {
					return resp, err
				}
				wait := base << attempt
				if resp != nil {
					if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
						wait = time.Duration(s) * time.Second
					}
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				select {
				case <-time.After(wait):
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
		})
	}
}

func retryable(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// SensitiveWords mark header and query parameter names whose values are
// redacted by Debug and in curl output (matched case-insensitively as substrings).
var SensitiveWords = []string{"auth", "key", "token", "secret", "password", "cookie", "session", "signature", "credential"}

// IsSensitive reports whether a header or query parameter name looks like it carries a secret.
func IsSensitive(name string) bool {
	name = strings.ToLower(name)
	for _, w := range SensitiveWords {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

// Debug dumps requests and responses to w with sensitive values redacted.
func Debug(w io.Writer) Middleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			p := style.For(w)
			out, in := p.Cyan("> "), p.Magenta("< ")
			if dump, err := httputil.DumpRequestOut(Redact(r), true); err == nil {
				fmt.Fprintf(w, "%s%s\n\n", out, indent(string(dump), out))
			}
			start := time.Now()
			resp, err := next.RoundTrip(r)
			if err != nil {
				fmt.Fprintf(w, "%s%s\n", in, p.Red(fmt.Sprintf("error after %s: %v", time.Since(start), err)))
				return resp, err
			}
			shown := *resp
			shown.Header = redactHeader(resp.Header)
			if dump, err := httputil.DumpResponse(&shown, false); err == nil {
				fmt.Fprintf(w, "%s%s%s\n\n", in, indent(string(dump), in), p.Dim("("+time.Since(start).Round(time.Millisecond).String()+")"))
			}
			return resp, nil
		})
	}
}

// Redact returns a copy of r with sensitive header and query values replaced.
// The body is preserved (via GetBody) so it can still be dumped.
func Redact(r *http.Request) *http.Request {
	c := r.Clone(r.Context())
	if r.GetBody != nil {
		c.Body, _ = r.GetBody()
	}
	c.Header = redactHeader(r.Header)
	q := c.URL.Query()
	changed := false
	for k := range q {
		if IsSensitive(k) {
			q.Set(k, "REDACTED")
			changed = true
		}
	}
	if changed {
		u := *c.URL
		u.RawQuery = q.Encode()
		c.URL = &u
	}
	return c
}

func redactHeader(h http.Header) http.Header {
	out := h.Clone()
	for k := range out {
		if IsSensitive(k) {
			out[k] = []string{"REDACTED"}
		}
	}
	return out
}

func indent(s, prefix string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\r\n"), "\n", "\n"+prefix) + "\n"
}

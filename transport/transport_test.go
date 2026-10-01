package transport

import (
	"net/http"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.test/x?api_key=s3cret&limit=5", strings.NewReader("body"))
	req.Header.Set("api_key", "s3cret")
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Accept", "application/json")

	r := Redact(req)
	if strings.Contains(r.URL.String(), "s3cret") || !strings.Contains(r.URL.String(), "limit=5") {
		t.Errorf("url not redacted correctly: %s", r.URL)
	}
	for k, vs := range r.Header {
		if strings.Contains(strings.Join(vs, ""), "s3cret") {
			t.Errorf("header %s leaked", k)
		}
	}
	if r.Header.Get("Accept") != "application/json" {
		t.Error("non-sensitive header changed")
	}
	if req.Header.Get("Authorization") != "Bearer s3cret" || !strings.Contains(req.URL.RawQuery, "s3cret") {
		t.Error("original request was mutated")
	}
}

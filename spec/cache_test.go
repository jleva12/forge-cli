package spec

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// specServer serves files with ETags and counts requests per path.
type specServer struct {
	*httptest.Server
	mu       sync.Mutex
	files    map[string]string
	status   int           // if set, every request fails with it
	block    chan struct{} // if set, requests wait for it to close
	requests map[string]int
	full     map[string]int // requests answered with the body, not 304
}

func newSpecServer(t *testing.T, files map[string]string) *specServer {
	s := &specServer{files: files, requests: map[string]int{}, full: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests[r.URL.Path]++
		body, ok := s.files[r.URL.Path]
		status, block := s.status, s.block
		s.mu.Unlock()
		if block != nil {
			<-block
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body)))
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.mu.Lock()
		s.full[r.URL.Path]++
		s.mu.Unlock()
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *specServer) set(path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = body
}

func (s *specServer) fail(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *specServer) counts(path string) (requests, full int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[path], s.full[path]
}

func doc(title string) string {
	return `{"openapi": "3.0.3", "info": {"title": "` + title + `", "version": "1"}, "paths": {}}`
}

func loadTitle(t *testing.T, location string, c *Cache) (string, error) {
	t.Helper()
	api, err := Load(context.Background(), location, LoadOptions{Cache: c})
	if err != nil {
		return "", err
	}
	return api.Title, nil
}

func TestCacheUsesFreshCopyWithoutDownloading(t *testing.T) {
	srv := newSpecServer(t, map[string]string{"/openapi.json": doc("v1")})
	c := &Cache{Dir: t.TempDir(), MaxAge: time.Hour}
	for range 3 {
		if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v1" {
			t.Fatalf("load = %q, %v", title, err)
		}
	}
	if requests, _ := srv.counts("/openapi.json"); requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}

	// A fresh copy works with the server gone.
	srv.Close()
	if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v1" {
		t.Errorf("offline load = %q, %v", title, err)
	}
}

func TestCacheRechecksOldCopies(t *testing.T) {
	srv := newSpecServer(t, map[string]string{"/openapi.json": doc("v1")})
	c := &Cache{Dir: t.TempDir()} // MaxAge 0: always check
	for range 2 {
		if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v1" {
			t.Fatalf("load = %q, %v", title, err)
		}
	}
	if requests, full := srv.counts("/openapi.json"); requests != 2 || full != 1 {
		t.Errorf("requests = %d (full %d), want 2 with 1 full download and one 304", requests, full)
	}

	srv.set("/openapi.json", doc("v2"))
	if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v2" {
		t.Errorf("after the spec changed: %q, %v", title, err)
	}
}

func TestCacheFallsBackToOldCopy(t *testing.T) {
	srv := newSpecServer(t, map[string]string{"/openapi.json": doc("v1")})
	var warned []string
	c := &Cache{Dir: t.TempDir(), Stale: func(url string, fetched time.Time, err error) {
		warned = append(warned, fmt.Sprintf("%s: %v", url, err))
	}}
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil {
		t.Fatal(err)
	}

	srv.fail(http.StatusBadGateway)
	if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v1" {
		t.Fatalf("with the server failing, load = %q, %v", title, err)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "502") {
		t.Errorf("warnings = %q", warned)
	}

	// Refresh asks for the latest, so an old copy won't do.
	c.Refresh = true
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err == nil {
		t.Error("refresh should fail when the server does")
	}

	// Without a copy there's nothing to fall back to.
	if _, err := loadTitle(t, srv.URL+"/openapi.json", &Cache{Dir: t.TempDir()}); err == nil {
		t.Error("load without a cached copy should fail when the server does")
	}
}

func TestCacheDoesNotWaitForUnresponsiveServer(t *testing.T) {
	defer func(d time.Duration) { staleTimeout = d }(staleTimeout)
	staleTimeout = 50 * time.Millisecond

	srv := newSpecServer(t, map[string]string{"/openapi.json": doc("v1")})
	var warning error
	c := &Cache{Dir: t.TempDir(), Stale: func(_ string, _ time.Time, err error) { warning = err }}
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil {
		t.Fatal(err)
	}

	block := make(chan struct{})
	defer close(block)
	srv.mu.Lock()
	srv.block = block
	srv.mu.Unlock()
	if title, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil || title != "v1" {
		t.Fatalf("load = %q, %v", title, err)
	}
	if warning == nil || !strings.Contains(warning.Error(), "no response within") {
		t.Errorf("warning = %v", warning)
	}
}

func TestCacheKeepsWorkingCopyWhenDownloadIsBroken(t *testing.T) {
	srv := newSpecServer(t, map[string]string{"/openapi.json": "not a spec"})
	c := &Cache{Dir: t.TempDir()}
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err == nil {
		t.Fatal("a broken spec should fail to load")
	}
	if c.read(srv.URL+"/openapi.json") != nil {
		t.Error("a spec that failed to load was cached")
	}

	srv.set("/openapi.json", doc("v1"))
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err != nil {
		t.Fatal(err)
	}
	srv.set("/openapi.json", "broken again")
	if _, err := loadTitle(t, srv.URL+"/openapi.json", c); err == nil {
		t.Fatal("a broken spec should fail to load")
	}
	if e := c.read(srv.URL + "/openapi.json"); e == nil || !strings.Contains(string(e.body), `"v1"`) {
		t.Errorf("the working copy was replaced: %+v", e)
	}
}

func TestCacheCoversExternalRefs(t *testing.T) {
	root := `{"openapi": "3.0.3", "info": {"title": "refs", "version": "1"}, "paths": {"/pets": {"get": {
  "operationId": "listPets",
  "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "schemas.json#/Pet"}}}}}}}}}`
	srv := newSpecServer(t, map[string]string{
		"/openapi.json": root,
		"/schemas.json": `{"Pet": {"type": "object", "properties": {"name": {"type": "string"}}}}`,
	})
	c := &Cache{Dir: t.TempDir(), MaxAge: time.Hour}
	for range 2 {
		api, err := Load(context.Background(), srv.URL+"/openapi.json", LoadOptions{Cache: c})
		if err != nil {
			t.Fatal(err)
		}
		if len(api.Operations) != 1 {
			t.Fatalf("operations = %d", len(api.Operations))
		}
	}
	for _, path := range []string{"/openapi.json", "/schemas.json"} {
		if requests, _ := srv.counts(path); requests != 1 {
			t.Errorf("%s requested %d times, want 1", path, requests)
		}
	}
}

func TestLoadWithoutCacheDownloadsEachTime(t *testing.T) {
	srv := newSpecServer(t, map[string]string{"/openapi.json": doc("v1")})
	for range 2 {
		if _, err := loadTitle(t, srv.URL+"/openapi.json", nil); err != nil {
			t.Fatal(err)
		}
	}
	if requests, _ := srv.counts("/openapi.json"); requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

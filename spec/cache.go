package spec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// Cache keeps specs downloaded from URLs on disk, so a CLI doesn't download
// its spec every time it runs. That covers the spec and any files its $refs
// point to by URL.
//
// A copy younger than MaxAge is used without contacting the server. An older
// one is checked with a conditional request (ETag or Last-Modified), so an
// unchanged spec isn't downloaded again. If the server can't be reached, the
// old copy is used instead of failing.
//
// Copies are saved only once the whole spec has loaded, so a broken download
// never replaces a working copy. Saving is best effort: if the directory isn't
// writable, specs are simply downloaded each time.
type Cache struct {
	// Dir holds one file per URL. It's created when needed.
	Dir string
	// MaxAge is how long a copy is used without checking the server. Zero
	// checks every time.
	MaxAge time.Duration
	// Refresh checks the server even when the copy is fresh, and fails
	// rather than falling back to an old copy.
	Refresh bool
	// Stale, if set, is called when an old copy is used because the server
	// couldn't be reached. fetched is when that copy was downloaded.
	Stale func(url string, fetched time.Time, err error)
}

// staleTimeout is how long to wait for a server to respond when an old copy
// could be used instead.
var staleTimeout = 10 * time.Second

// cacheEntry is a cached download. On disk it's a JSON header line followed
// by the body.
type cacheEntry struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Fetched      time.Time `json:"fetched"`
	body         []byte
}

func (c *Cache) path(location string) string {
	sum := sha256.Sum256([]byte(location))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:16])+".spec")
}

// read returns the cached copy of location, or nil if there's none.
func (c *Cache) read(location string) *cacheEntry {
	data, err := os.ReadFile(c.path(location))
	if err != nil {
		return nil
	}
	header, body, ok := bytes.Cut(data, []byte("\n"))
	var e cacheEntry
	if !ok || json.Unmarshal(header, &e) != nil || e.URL != location {
		return nil
	}
	e.body = body
	return &e
}

// write saves e atomically, so a concurrent run never reads a partial file.
func (c *Cache) write(e *cacheEntry) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	header, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, "*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(append(header, '\n'), e.body...))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.path(e.URL))
}

// fetcher reads the spec and its external $refs for one load, through the
// cache when there is one.
type fetcher struct {
	ctx     context.Context
	client  *http.Client
	cache   *Cache
	pending []*cacheEntry // saved once the spec has loaded
}

func newFetcher(ctx context.Context, opts LoadOptions) *fetcher {
	return &fetcher{ctx: ctx, client: opts.HTTPClient, cache: opts.Cache}
}

// get returns the contents of an http(s) URL.
func (f *fetcher) get(location string) ([]byte, error) {
	c := f.cache
	if c == nil {
		e, err := fetch(f.ctx, f.client, location, nil, 0)
		if err != nil {
			return nil, err
		}
		return e.body, nil
	}
	old := c.read(location)
	if old != nil && !c.Refresh && time.Since(old.Fetched) < c.MaxAge {
		return old.body, nil
	}
	var wait time.Duration
	if old != nil && !c.Refresh {
		wait = staleTimeout
	}
	e, err := fetch(f.ctx, f.client, location, old, wait)
	if err != nil {
		if old == nil || c.Refresh || f.ctx.Err() != nil {
			return nil, err
		}
		if c.Stale != nil {
			c.Stale(location, old.Fetched, err)
		}
		return old.body, nil
	}
	f.pending = append(f.pending, e)
	return e.body, nil
}

// readFromURI reads external $refs: URLs with get, files from disk.
func (f *fetcher) readFromURI(loader *openapi3.Loader, location *url.URL) ([]byte, error) {
	if location.Scheme == "http" || location.Scheme == "https" {
		return f.get(location.String())
	}
	return openapi3.ReadFromFile(loader, location)
}

// save stores what was downloaded or rechecked during the load.
func (f *fetcher) save() {
	for _, e := range f.pending {
		f.cache.write(e) // best effort: worst case, it's downloaded again next time
	}
}

// fetch GETs location. With a cached copy, it asks only for a newer version,
// and returns the copy marked as just checked if the server has none. With
// wait, the server must start responding within that time.
func fetch(ctx context.Context, client *http.Client, location string, old *cacheEntry, wait time.Duration) (*cacheEntry, error) {
	if client == nil {
		client = http.DefaultClient
	}
	parent, stopTimer := ctx, func() bool { return false }
	if wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		stopTimer = time.AfterFunc(wait, cancel).Stop
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/yaml;q=0.9, */*;q=0.5")
	if old != nil {
		if old.ETag != "" {
			req.Header.Set("If-None-Match", old.ETag)
		}
		if old.LastModified != "" {
			req.Header.Set("If-Modified-Since", old.LastModified)
		}
	}
	resp, err := client.Do(req)
	stopTimer() // it's responding: let the body take as long as it needs
	if err != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			err = fmt.Errorf("no response within %s", wait)
		}
		return nil, err
	}
	defer resp.Body.Close()
	now := time.Now().UTC()
	if resp.StatusCode == http.StatusNotModified && old != nil {
		e := *old
		e.Fetched = now
		return &e, nil
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &cacheEntry{
		URL:          location,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Fetched:      now,
		body:         body,
	}, nil
}

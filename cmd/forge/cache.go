package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jleva12/forge-cli/spec"
)

// defaultSpecMaxAge is how long a downloaded spec is used before forge checks
// the server for a newer one.
const defaultSpecMaxAge = 24 * time.Hour

// specCache keeps specs registered by URL between runs, in
// $XDG_CACHE_HOME/forge/specs or the OS cache directory
// (~/Library/Caches/forge/specs on macOS). It's nil when there's no cache
// directory, and specs are then downloaded every time.
func specCache() (*spec.Cache, error) {
	maxAge := defaultSpecMaxAge
	if v := os.Getenv("FORGE_SPEC_MAX_AGE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("FORGE_SPEC_MAX_AGE=%q: want a duration such as 30m or 24h, or 0 to check every time", v)
		}
		maxAge = d
	}
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		var err error
		if dir, err = os.UserCacheDir(); err != nil {
			return nil, nil
		}
	}
	return &spec.Cache{Dir: filepath.Join(dir, "forge", "specs"), MaxAge: maxAge}, nil
}

// staleWarning explains that an old copy of a spec is being used.
func staleWarning(location string, fetched time.Time, err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err // the URL is already in the message
	}
	return fmt.Sprintf("Couldn't check %s for updates (%v); using the copy downloaded %s.", location, err, ago(fetched))
}

// ago describes roughly how long ago t was.
func ago(t time.Time) string {
	switch d := time.Since(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return unit(int(d.Minutes()), "minute") + " ago"
	case d < 48*time.Hour:
		return unit(int(d.Hours()), "hour") + " ago"
	default:
		return unit(int(d.Hours()/24), "day") + " ago"
	}
}

func unit(n int, name string) string {
	if n == 1 {
		return "1 " + name
	}
	return fmt.Sprintf("%d %ss", n, name)
}

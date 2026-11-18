package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const CatalogMaxAge = 30 * time.Minute
const bmcl = "https://bmclapi2.bangbang93.com"

type catalogSource[T any] func(context.Context) (T, error)

// Use the first successful response and cancel the remaining requests.
func firstCatalog[T any](ctx context.Context, sources ...catalogSource[T]) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	type response struct {
		value T
		err   error
	}
	results := make(chan response, len(sources))
	for _, fetch := range sources {
		go func() {
			value, err := fetch(ctx)
			results <- response{value, err}
		}()
	}
	finished := 0
	var last error
	for finished < len(sources) {
		select {
		case <-ctx.Done():
			var empty T
			return empty, ctx.Err()
		case result := <-results:
			if result.err == nil {
				return result.value, nil
			}
			last = errors.Join(last, result.err)
			finished++
		}
	}
	var empty T
	return empty, last
}

func preferCatalog[T any](ctx context.Context, sources ...catalogSource[T]) (T, error) {
	var failures error
	for _, fetch := range sources {
		if ctx.Err() != nil {
			return *new(T), ctx.Err()
		}
		attempt, cancel := context.WithTimeout(ctx, 8*time.Second)
		value, err := fetch(attempt)
		cancel()
		if err == nil {
			return value, nil
		}
		failures = errors.Join(failures, err)
	}
	var empty T
	return empty, failures
}

func mirroredJSON[T any](ctx context.Context, mirror, official string, out *T) error {
	source := func(address string) catalogSource[T] {
		return func(ctx context.Context) (T, error) {
			var result T
			err := catalogJSON(ctx, address, &result)
			return result, err
		}
	}
	value, err := preferCatalog(ctx, source(mirror), source(official))
	if err == nil {
		*out = value
	}
	return err
}

func catalogJSON(ctx context.Context, address string, out any) error {
	return catalogRequest(ctx, http.MethodGet, address, nil, out)
}

func catalogRequest(ctx context.Context, method, address string, request io.Reader, out any) error {
	res, err := requestHTTP(ctx, method, address, "", request)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body := io.LimitReader(res.Body, 8<<20)
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, body)
	return nil
}

func (e Engine) catalogPath(name string) string {
	return filepath.Join(e.DataDir, "catalog", name+".json")
}

func readCatalog[T any](path string) (list []T, modified time.Time, found bool) {
	if readJSON(path, &list) != nil {
		return nil, time.Time{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, false
	}
	return list, info.ModTime(), true
}

func storeCatalog[T any](path string, list []T) {
	if os.MkdirAll(filepath.Dir(path), 0700) == nil {
		_ = writeJSON(path, list)
	}
}

func (e Engine) CachedVersions() ([]Version, time.Time) {
	versions, stamp, _ := readCatalog[Version](e.catalogPath("minecraft"))
	return versions, stamp
}

func (e Engine) RefreshVersions(ctx context.Context) ([]Version, error) {
	versions, err := Versions(ctx)
	if err == nil {
		storeCatalog(e.catalogPath("minecraft"), versions)
	}
	return versions, err
}

func (e Engine) buildCatalogPath(version, loader string) string {
	return e.catalogPath(filepath.Join("loaders", loader, version))
}

func (e Engine) CachedBuilds(version, loader string) ([]LoaderBuild, time.Time, bool) {
	if !segment(version) || !segment(loader) {
		return nil, time.Time{}, false
	}
	return readCatalog[LoaderBuild](e.buildCatalogPath(version, loader))
}

func (e Engine) RefreshBuilds(ctx context.Context, version, loader string) ([]LoaderBuild, error) {
	builds, err := LoaderBuilds(ctx, version, loader)
	if err == nil {
		storeCatalog(e.buildCatalogPath(version, loader), builds)
	}
	return builds, err
}

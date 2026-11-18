package engine

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

type sourcePreferences struct {
	mu     sync.Mutex
	routes map[string]*sourceChoice
}

type sourceChoice struct {
	ready  chan struct{}
	winner int
}

type sourcePreferencesKey struct{}

func withSourcePreferences(ctx context.Context) context.Context {
	if ctx.Value(sourcePreferencesKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, sourcePreferencesKey{}, &sourcePreferences{routes: make(map[string]*sourceChoice)})
}

func (a artifact) orderedSources(ctx context.Context) []string {
	sources := append([]string{a.URL}, a.Mirrors...)
	if len(sources) < 2 || ctx.Err() != nil {
		return sources
	}
	preferences, _ := withSourcePreferences(ctx).Value(sourcePreferencesKey{}).(*sourcePreferences)
	var routes []string
	for _, source := range sources {
		u, err := url.Parse(source)
		if err != nil || u.Host == "" {
			return sources
		}
		routes = append(routes, u.Scheme+"://"+u.Host)
	}
	key := strings.Join(routes, "|")
	preferences.mu.Lock()
	choice := preferences.routes[key]
	measure := choice == nil
	if measure {
		choice = &sourceChoice{ready: make(chan struct{})}
		preferences.routes[key] = choice
	}
	preferences.mu.Unlock()
	if measure {
		choice.winner = fastestSource(ctx, sources, a.Size)
		close(choice.ready)
	}
	select {
	case <-choice.ready:
		if choice.winner > 0 {
			preferred := sources[choice.winner]
			copy(sources[1:choice.winner+1], sources[:choice.winner])
			sources[0] = preferred
		}
	case <-ctx.Done():
	}
	return sources
}

func fastestSource(ctx context.Context, sources []string, size int64) int {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	const sample = 256 << 10
	checks := make([]catalogSource[int], 0, len(sources))
	for index, source := range sources {
		checks = append(checks, func(ctx context.Context) (int, error) {
			var byteRange string
			if size <= 0 || size > sample {
				byteRange = fmt.Sprintf("bytes=0-%d", sample-1)
			}
			res, err := getHTTP(ctx, source, byteRange)
			if err != nil {
				return 0, err
			}
			defer res.Body.Close()
			_, err = io.Copy(io.Discard, io.LimitReader(res.Body, sample))
			return index, err
		})
	}
	winner, _ := firstCatalog(ctx, checks...)
	return winner
}

func withMinecraftMirror(file artifact, mirror string) artifact {
	if mirror != "" && (file.SHA1 != "" || file.SHA256 != "" || file.SHA512 != "") {
		file.Mirrors = append(slices.Clone(file.Mirrors), mirror)
	}
	return file
}

func minecraftMirror(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path == "" {
		return ""
	}
	var prefix string
	switch u.Host {
	case "piston-meta.mojang.com", "piston-data.mojang.com", "launchermeta.mojang.com", "launcher.mojang.com":
	case "resources.download.minecraft.net":
		prefix = "/assets"
	case "libraries.minecraft.net", "maven.fabricmc.net", "maven.minecraftforge.net":
		prefix = "/maven"
	default:
		return ""
	}
	return bmcl + prefix + u.RequestURI()
}

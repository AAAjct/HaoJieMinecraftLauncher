package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	goversion "go/version"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type LoaderBuild struct {
	Version     string
	Stable      bool
	Recommended bool
}

type loaderCatalogRow struct{ Loader LoaderBuild }

var modLoaders = []string{"fabric", "forge", "neoforge"}

func SupportedModLoader(loader string) bool {
	return slices.Contains(modLoaders, loader)
}

func LoaderBuilds(ctx context.Context, version, loader string) ([]LoaderBuild, error) {
	if !segment(version) {
		return nil, fmt.Errorf("无效的 Minecraft 版本")
	}
	switch loader {
	case "forge":
		return forgeBuilds(ctx, version)
	case "neoforge":
		return neoForgeBuilds(ctx, version)
	}
	if !SupportedModLoader(loader) {
		return nil, fmt.Errorf("不提供 %s 加载器下载", loader)
	}
	base := loaderURL(loader)
	source := func(base string) catalogSource[[]loaderCatalogRow] {
		return func(ctx context.Context) ([]loaderCatalogRow, error) {
			var rows []loaderCatalogRow
			err := catalogJSON(ctx, base+url.PathEscape(version), &rows)
			return rows, err
		}
	}
	sources := []catalogSource[[]loaderCatalogRow]{source(base)}
	if loader == "fabric" {
		sources = append([]catalogSource[[]loaderCatalogRow]{source(bmcl + "/fabric-meta/v2/versions/loader/")}, sources...)
	}
	response, err := preferCatalog(ctx, sources...)
	if err != nil {
		var failure httpFailure
		if errors.As(err, &failure) && failure.status == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 %s 构建：%w", loader, err)
	}
	var builds []LoaderBuild
	seen := map[string]bool{}
	for _, row := range response {
		build := row.Loader
		if !segment(build.Version) || seen[build.Version] {
			continue
		}
		seen[build.Version] = true
		builds = append(builds, build)
	}
	sortLoaderBuilds(builds)
	return builds, nil
}

func sortLoaderBuilds(builds []LoaderBuild) {
	slices.SortStableFunc(builds, func(a, b LoaderBuild) int {
		return compareBuild(b.Version, a.Version)
	})
}

func compareBuild(a, b string) int {
	aCore, aPre, _ := strings.Cut(a, "-")
	bCore, bPre, _ := strings.Cut(b, "-")
	result := goversion.Compare("go"+aCore, "go"+bCore)
	if !goversion.IsValid("go"+aCore) || !goversion.IsValid("go"+bCore) {
		result = compareBuildParts(aCore, bCore)
	}
	if result != 0 {
		return result
	}
	if aPre == bPre {
		return cmp.Compare(a, b)
	}
	if aPre == "" {
		return 1
	}
	if bPre == "" {
		return -1
	}
	return compareBuildParts(aPre, bPre)
}

func compareBuildParts(a, b string) int {
	return slices.CompareFunc(strings.Split(a, "."), strings.Split(b, "."), func(a, b string) int {
		aNumber, aErr := strconv.Atoi(a)
		bNumber, bErr := strconv.Atoi(b)
		if aErr == nil && bErr == nil {
			return cmp.Compare(aNumber, bNumber)
		}
		return cmp.Compare(a, b)
	})
}

func forgeBuilds(ctx context.Context, version string) ([]LoaderBuild, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	preferred := make(chan string, 1)
	go func() {
		build, _ := recommendedForge(ctx, version)
		preferred <- build
	}()
	official := func(ctx context.Context) ([]LoaderBuild, error) {
		var metadata map[string][]string
		err := catalogJSON(ctx, "https://files.minecraftforge.net/net/minecraftforge/forge/maven-metadata.json", &metadata)
		return forgeBuildsForGame(version, metadata[version]), err
	}
	mirror := func(ctx context.Context) ([]LoaderBuild, error) {
		var rows []struct {
			Version, MCVersion string
		}
		err := catalogJSON(ctx, bmcl+"/forge/minecraft/"+url.PathEscape(version), &rows)
		coordinates := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.MCVersion == version {
				coordinates = append(coordinates, version+"-"+row.Version)
			}
		}
		return forgeBuildsForGame(version, coordinates), err
	}
	builds, err := preferCatalog(ctx, mirror, official)
	if err != nil {
		return nil, fmt.Errorf("读取 Forge 构建：%w", err)
	}
	if len(builds) == 0 {
		return builds, nil
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case recommended := <-preferred:
		for i := range builds {
			builds[i].Recommended = builds[i].Version == recommended
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return builds, nil
}

func forgeBuildsForGame(version string, coordinates []string) []LoaderBuild {
	var builds []LoaderBuild
	for _, coordinate := range coordinates {
		build, ok := strings.CutPrefix(coordinate, version+"-")
		if !ok || !forgeBuild.MatchString(build) {
			continue
		}
		major, _, _ := strings.Cut(build, ".")
		number, _ := strconv.Atoi(major)
		// Forge builds before the 1.13.2 generation use the legacy installer format.
		if number >= 25 {
			builds = append(builds, LoaderBuild{Version: build, Stable: true})
		}
	}
	sortLoaderBuilds(builds)
	return builds
}

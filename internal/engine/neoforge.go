package engine

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const neoForgeMaven = "https://maven.neoforged.net/releases/net/neoforged/"

var modernNeoForgeBuild = regexp.MustCompile("^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+(?:-(?:beta|alpha\\.[0-9]+)(?:\\+(?:snapshot|pre|rc)-[0-9]+)?)?$")
var legacyNeoForgeBuild = regexp.MustCompile("^[0-9]+\\.[0-9]+\\.[0-9]+(?:-beta)?$")

func neoForgeArtifact(game string) string {
	if game == "1.20.1" {
		return "forge"
	}
	return "neoforge"
}

func neoForgeBuilds(ctx context.Context, game string) ([]LoaderBuild, error) {
	address := neoForgeMaven + neoForgeArtifact(game) + "/maven-metadata.xml"
	source := func(address string) catalogSource[[]LoaderBuild] {
		return func(ctx context.Context) ([]LoaderBuild, error) {
			res, err := get(ctx, address)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			var metadata struct {
				Versions []string `xml:"versioning>versions>version"`
			}
			if err := xml.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&metadata); err != nil {
				return nil, err
			}
			if len(metadata.Versions) == 0 {
				return nil, fmt.Errorf("NeoForge 版本列表为空")
			}
			return neoForgeBuildsForGame(game, metadata.Versions), nil
		}
	}
	return preferCatalog(ctx, source(minecraftMirror(address)), source(address))
}

func neoForgeBuildsForGame(game string, versions []string) []LoaderBuild {
	var builds []LoaderBuild
	for _, version := range versions {
		build := version
		if game == "1.20.1" {
			if !strings.HasPrefix(version, game+"-") {
				continue
			}
			build = strings.TrimPrefix(version, game+"-")
		}
		if neoForgeMatches(game, build) {
			builds = append(builds, LoaderBuild{Version: build, Stable: !strings.Contains(build, "-")})
		}
	}
	sortLoaderBuilds(builds)
	return builds
}

func neoForgeMatches(game, build string) bool {
	if game == "1.20.1" {
		return forgeBuild.MatchString(build)
	}
	core, suffix, _ := strings.Cut(build, "-")
	parts := strings.Split(core, ".")
	if modernNeoForgeBuild.MatchString(build) {
		version := strings.Join(parts[:2], ".")
		if parts[2] != "0" {
			version += "." + parts[2]
		}
		if _, snapshot, ok := strings.Cut(suffix, "+"); ok {
			version += "-" + snapshot
		}
		return game == version
	}
	if legacyNeoForgeBuild.MatchString(build) {
		version := "1." + parts[0]
		if parts[1] != "0" {
			version += "." + parts[1]
		}
		return game == version
	}
	return false
}

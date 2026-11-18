package engine

import (
	"net/url"
	"slices"
	"strings"
)

const mcim = "https://mod.mcimirror.top"

func (a artifact) orderedSources() []string {
	sources := slices.Clone(a.Mirrors)
	if mirror := resourceMirror(a.URL); mirror != "" && (a.SHA1 != "" || a.SHA512 != "") {
		sources = append(sources, mirror)
	}
	return append(append(sources, a.URL), a.Alternates...)
}

func resourceMirror(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	switch u.Host {
	case "cdn.modrinth.com", "edge.forgecdn.net", "mediafilez.forgecdn.net":
		// Only forward the file path. Official URLs can carry credentials.
		return mcim + u.EscapedPath()
	}
	return ""
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
	case "maven.neoforged.net":
		if remainder, ok := strings.CutPrefix(u.RequestURI(), "/releases/"); ok {
			return bmcl + "/maven/" + remainder
		}
		return ""
	case "resources.download.minecraft.net":
		prefix = "/assets"
	case "libraries.minecraft.net", "maven.fabricmc.net", "maven.minecraftforge.net":
		prefix = "/maven"
	default:
		return ""
	}
	return bmcl + prefix + u.RequestURI()
}

package engine

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestForgeBuildsMatchMinecraftVersion(t *testing.T) {
	builds := forgeBuildsForGame("1.21.1", []string{
		"1.21.1-52.0.9", "1.21.1-52.0.10", "1.21.1-52.1.2", "1.21.10-60.0.0", "1.21.1-14.23.5",
	})
	if len(builds) != 3 || builds[0].Version != "52.1.2" || builds[1].Version != "52.0.10" {
		t.Fatalf("wrong Forge build list: %+v", builds)
	}
}

func TestNeoForgeBuildsMatchMinecraftVersion(t *testing.T) {
	versions := []string{
		"1.20.1-47.1.106", "20.2.59", "21.0.19-beta", "21.1.267",
		"26.1.0.0-alpha.14+snapshot-11", "26.1.0.7-beta", "26.1.1.2",
		"26.3.0.9", "26.3.0.10", "26.3.1.2", "26.3.0.1-beta",
	}
	for _, test := range []struct {
		game string
		want []string
	}{
		{"1.20.1", []string{"47.1.106"}},
		{"1.20.2", []string{"20.2.59"}},
		{"1.21", []string{"21.0.19-beta"}},
		{"1.21.1", []string{"21.1.267"}},
		{"26.1-snapshot-11", []string{"26.1.0.0-alpha.14+snapshot-11"}},
		{"26.1", []string{"26.1.0.7-beta"}},
		{"26.3", []string{"26.3.0.10", "26.3.0.9", "26.3.0.1-beta"}},
	} {
		var names []string
		for _, build := range neoForgeBuildsForGame(test.game, versions) {
			names = append(names, build.Version)
		}
		if !slices.Equal(names, test.want) {
			t.Fatalf("NeoForge %s: got %v, want %v", test.game, names, test.want)
		}
	}
}

func TestNeoForgeInstallerFallsBackToForgeCDN(t *testing.T) {
	const path = "/net/neoforged/neoforge/21.1.264/neoforge-21.1.264-installer.jar"
	const jar = "NeoForge installer fixture"
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	var requests []string
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		address := req.URL.Host + req.URL.Path
		requests = append(requests, address)
		status, body := http.StatusOK, ""
		switch address {
		case "bmclapi2.bangbang93.com/maven" + path + ".sha1":
			status = http.StatusNotFound
		case "neoforged.forgecdn.net/releases" + path + ".sha1":
			body = fmt.Sprintf("%x", sha1.Sum([]byte(jar)))
		case "bmclapi2.bangbang93.com/maven" + path:
			body = "incorrect installer contents"
		case "neoforged.forgecdn.net/releases" + path:
			body = jar
		default:
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	target := filepath.Join(t.TempDir(), "installer.jar")
	if err := downloadForgeInstaller(t.Context(), "https://maven.neoforged.net/releases"+path, target); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != jar {
		t.Fatalf("installer = %q, err = %v", got, err)
	}
	want := []string{
		"bmclapi2.bangbang93.com/maven" + path + ".sha1",
		"neoforged.forgecdn.net/releases" + path + ".sha1",
		"bmclapi2.bangbang93.com/maven" + path,
		"neoforged.forgecdn.net/releases" + path,
	}
	if !slices.Equal(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestLiveLoaderCatalogs(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to query the loader catalogs")
	}
	for _, item := range []struct{ loader, known string }{
		{"forge", "52.1.20"}, {"neoforge", "21.1.200"}, {"fabric", "0.16.10"},
	} {
		builds, err := LoaderBuilds(t.Context(), "1.21.1", item.loader)
		if err != nil || len(builds) < 2 || !slices.ContainsFunc(builds, func(b LoaderBuild) bool { return b.Version == item.known }) {
			t.Fatalf("%s did not return known alternatives: %d builds, %v", item.loader, len(builds), err)
		}
		t.Logf("%s: %d builds, first %s", item.loader, len(builds), builds[0].Version)
	}
}

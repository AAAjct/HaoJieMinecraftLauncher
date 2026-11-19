package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackOverrides(t *testing.T) {
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, entry := range []struct{ path, text string }{
		{"overrides/config/example.txt", "common"},
		{"server-overrides/config/example.txt", "server"},
		{"client-overrides/config/example.txt", "client"},
	} {
		file, err := archive.Create(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(entry.text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "example.mrpack")
	if err := os.WriteFile(archivePath, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, prefix := range []string{"overrides/", "client-overrides/"} {
		if err := unpackOverrides(t.Context(), archivePath, root, prefix); err != nil {
			t.Fatal(err)
		}
	}
	out, err := os.ReadFile(filepath.Join(root, "config", "example.txt"))
	if err != nil || string(out) != "client" {
		t.Fatalf("client override: %q, %v", out, err)
	}
	for _, name := range []string{"../escape", "mods/../../escape", "mods\\evil.jar", "C:/escape", "mods/a:stream"} {
		if _, err := localPath(root, name); err == nil {
			t.Fatalf("accepted unsafe path %q", name)
		}
	}
}

func TestPackImportCreatesIsolatedInstance(t *testing.T) {
	e := packTestEngine(t)
	version := "test-version"
	path := filepath.Join(t.TempDir(), "example.mrpack")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(file)
	for name, content := range map[string]string{
		"modrinth.index.json":        `{"formatVersion":1,"game":"minecraft","name":"Fixture pack","dependencies":{"minecraft":"test-version"}}`,
		"overrides/mods/example.jar": "pack mod",
	} {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	checkedDuringInstall := false
	first, err := e.ImportPack(t.Context(), path, func(message string) {
		if message == "正在读取整合包清单…" || checkedDuringInstall {
			return
		}
		checkedDuringInstall = true
		root := filepath.Join(e.GameRoot, "versions")
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 || entries[0].Name() != "example" {
			t.Errorf("versions during install = %v, err = %v, want only example", entries, err)
		}
		if installed, err := e.Instances(); err != nil || len(installed) != 0 {
			t.Errorf("incomplete instance became visible: %v, err = %v", installed, err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "Fixture pack" || first.ID != "example" || first.Java != 8 {
		t.Fatalf("unexpected first instance: %+v", first)
	}
	if !checkedDuringInstall || !regularFile(e.baseVersionFile(version, ".jar")) {
		t.Fatal("install did not use the named directory and migrated client cache")
	}
	if _, err := os.Stat(filepath.Join(e.GameDir(first), installingMarker)); !os.IsNotExist(err) {
		t.Fatalf("finished instance has an installation marker: %v", err)
	}
	type result struct {
		instance Instance
		err      error
	}
	results := make(chan result, 2)
	go func() {
		instance, err := e.ImportPack(t.Context(), path, func(string) {})
		results <- result{instance, err}
	}()
	go func() {
		instance, err := e.Install(t.Context(), Instance{Name: "Fixture pack", Version: version}, func(string) {})
		results <- result{instance, err}
	}()
	instances := []Instance{first}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		instances = append(instances, result.instance)
	}
	if !slices.ContainsFunc(instances, func(i Instance) bool { return i.Name == "Fixture pack(1)" }) ||
		!slices.ContainsFunc(instances, func(i Instance) bool { return i.Name == "Fixture pack(2)" }) {
		t.Fatalf("concurrent installations did not allocate distinct names: %+v", instances)
	}
	if !slices.ContainsFunc(instances, func(i Instance) bool { return i.ID == "example(1)" }) {
		t.Fatalf("same archive filename should get a unique directory: %+v", instances)
	}
	for _, instance := range instances {
		if e.GameDir(instance) != filepath.Join(e.GameRoot, "versions", instance.ID) ||
			!regularFile(filepath.Join(e.GameDir(instance), instance.ID+".jar")) ||
			!regularFile(filepath.Join(e.GameDir(instance), instance.ID+".json")) {
			t.Fatalf("incorrect portable layout: %s", e.GameDir(instance))
		}
		var profile gameProfile
		if err := readJSON(filepath.Join(e.GameDir(instance), instance.ID+".json"), &profile); err != nil || profile.ID != instance.ID {
			t.Fatalf("profile ID = %q, want %q, read error = %v", profile.ID, instance.ID, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(e.GameDir(first), "mods", "example.jar"))
	if err != nil || string(content) != "pack mod" {
		t.Fatalf("pack contents: %q (%v)", content, err)
	}
	if err := e.RenameInstance(first, "fixture PACK"); err != nil {
		t.Fatal(err)
	}
	_, saved, err := e.instanceMetadata(first)
	if err != nil || saved.Name != "fixture PACK" {
		t.Fatalf("renaming the current instance should not add a suffix: %+v (%v)", saved, err)
	}
	fourth, err := e.Install(t.Context(), Instance{Name: "New instance", Version: version}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RenameInstance(fourth, "Fixture pack"); err != nil {
		t.Fatal(err)
	}
	_, saved, err = e.instanceMetadata(fourth)
	if err != nil || saved.Name != "Fixture pack(3)" {
		t.Fatalf("renaming should skip all occupied suffixes: %+v (%v)", saved, err)
	}
}

func packTestEngine(t *testing.T) Engine {
	t.Helper()
	e := Engine{DataDir: t.TempDir(), GameRoot: filepath.Join(t.TempDir(), ".minecraft")}
	version := "test-version"
	base := filepath.Join(e.GameRoot, "versions", version)
	for name, content := range map[string]string{
		filepath.Join(base, version+".json"):                               `{"id":"test-version","mainClass":"Main","downloads":{"client":{"url":"https://invalid.example/client.jar"}},"assetIndex":{"id":"test-assets","url":"https://invalid.example/index.json"}}`,
		filepath.Join(base, version+".jar"):                                "cached client",
		filepath.Join(e.GameRoot, "assets", "indexes", "test-assets.json"): "{}",
		filepath.Join(e.javaHome(8), "bin", "java.exe"):                    "cached java",
		filepath.Join(e.javaHome(8), "bin", "javac.exe"):                   "cached javac",
		filepath.Join(e.javaHome(8), "release"):                            "JAVA_VERSION=\"1.8.0_472\"\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestCurseForgePackImportsZIPAndRAR(t *testing.T) {
	e := packTestEngine(t)
	dir := t.TempDir()
	const storedRAR = "UmFyIRoHAM+QcwAADQAAAAAAAACGkXQAgDMAwAAAAMAAAAAC7l6OMwAAAAAUMBMAIAAAAEN1cnNlL21hbmlmZXN0Lmpzb257Im1hbmlmZXN0VHlwZSI6Im1pbmVjcmFmdE1vZHBhY2siLCJtYW5pZmVzdFZlcnNpb24iOjEsIm5hbWUiOiJDdXJzZSBmaXh0dXJlIiwibWluZWNyYWZ0Ijp7InZlcnNpb24iOiJ0ZXN0LXZlcnNpb24ifSwib3ZlcnJpZGVzIjoiY3VzdG9tIiwiZmlsZXMiOlt7InByb2plY3RJRCI6MCwiZmlsZUlEIjowLCJyZXF1aXJlZCI6ZmFsc2V9XX1trnQAgDwACgAAAAoAAAACmomuHgAAAAAUMBwAIAAAAEN1cnNlL2N1c3RvbS9jb25maWcvdGVzdC50eHRyYXIgY29uZmlnBLB7AAAHAA=="
	rar, err := base64.StdEncoding.DecodeString(storedRAR)
	if err != nil {
		t.Fatal(err)
	}
	rarPath := filepath.Join(dir, "example.RAR")
	if err := os.WriteFile(rarPath, rar, 0600); err != nil {
		t.Fatal(err)
	}
	instance, err := e.ImportPack(t.Context(), rarPath, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(e.GameDir(instance), "config", "test.txt"))
	if err != nil || string(config) != "rar config" || instance.Name != "Curse fixture" || instance.ID != "example" {
		t.Fatalf("RAR import: %q, %+v, %v", config, instance, err)
	}

	const mod = "fixture jar"
	const webMod = "fixture jar from CurseForge website"
	const cdn = "https://edge.forgecdn.net/files/12/34/mod.jar"
	const webCDN = "https://edge.forgecdn.net/files/12/35/web.jar?grant=test"
	t.Setenv("CURSEFORGE_API_KEY", "test-key")
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		code, body := http.StatusOK, ""
		switch req.URL.Host + req.URL.Path {
		case "mod.mcimirror.top/curseforge/v1/mods/files":
			if req.Method != http.MethodPost || req.Header.Get("X-Api-Key") != "" {
				return nil, fmt.Errorf("unexpected mirror request for metadata")
			}
			code = http.StatusServiceUnavailable
		case "api.curseforge.com/v1/mods/files":
			request, err := io.ReadAll(req.Body)
			req.Body.Close()
			if err != nil || string(request) != `{"fileIds":[1234,1235]}` || req.Header.Get("X-Api-Key") != "test-key" {
				return nil, fmt.Errorf("invalid official metadata request")
			}
			body = fmt.Sprintf(`{"data":[{"id":1234,"modId":42,"fileName":"mod.jar","downloadUrl":%q,"fileLength":%d,"hashes":[{"algo":1,"value":"%x"}]},{"id":1235,"modId":43,"fileName":"web.jar","downloadUrl":null,"fileLength":%d,"hashes":[{"algo":1,"value":"%x"}]}]}`,
				cdn, len(mod), sha1.Sum([]byte(mod)), len(webMod), sha1.Sum([]byte(webMod)))
		case "mod.mcimirror.top/files/12/34/mod.jar":
			if req.Header.Get("X-Api-Key") != "" {
				return nil, fmt.Errorf("official key sent to mirror")
			}
			code = http.StatusNotFound
		case "edge.forgecdn.net/files/12/34/mod.jar":
			if req.Header.Get("X-Api-Key") != "test-key" {
				return nil, fmt.Errorf("missing official CDN key")
			}
			body = mod
		case "www.curseforge.com/api/v1/mods/43/files/1235/download":
			if req.Header.Get("X-Api-Key") != "" {
				return nil, fmt.Errorf("official key sent to website")
			}
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": {webCDN}}, Body: http.NoBody, Request: req}, nil
		case "edge.forgecdn.net/files/12/35/web.jar":
			if req.URL.String() != webCDN {
				return nil, fmt.Errorf("invalid website download redirect")
			}
			body = webMod
		default:
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	entry, err := z.Create("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, `{"manifestType":"minecraftModpack","manifestVersion":1,"name":"Curse ZIP","minecraft":{"version":"test-version"},"files":[{"projectID":42,"fileID":1234,"required":true},{"projectID":43,"fileID":1235,"required":true}]}`); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	const packStem = "DeceasedCraft_Beta-5.10.17"
	zipPath := filepath.Join(dir, packStem+".zip")
	if err := os.WriteFile(zipPath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	instance, err = e.ImportPack(t.Context(), zipPath, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	expectedDir := filepath.Join(e.GameRoot, "versions", packStem)
	if dir := e.GameDir(instance); dir != expectedDir {
		t.Fatalf("ZIP version directory = %q, want %q", dir, expectedDir)
	}
	if !regularFile(filepath.Join(expectedDir, packStem+".jar")) || !regularFile(filepath.Join(expectedDir, packStem+".json")) {
		t.Fatalf("missing named launch files in %s", expectedDir)
	}
	result, err := os.ReadFile(filepath.Join(expectedDir, "mods", "mod.jar"))
	if err != nil || string(result) != mod || instance.ID != packStem || instance.Name != "Curse ZIP" {
		t.Fatalf("ZIP import: %q, %v", result, err)
	}
	result, err = os.ReadFile(filepath.Join(e.GameDir(instance), "mods", "web.jar"))
	if err != nil || string(result) != webMod {
		t.Fatalf("ZIP import through website: %q, %v", result, err)
	}
}

func TestPackVersionIDHandlesWindowsPaths(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Integrated Boogaloo", "Integrated Boogaloo(1)"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ title, want string }{
		{"Integrated Boogaloo", "Integrated Boogaloo(2)"},
		{"..\\My: Pack? ", "_My_ Pack_"},
		{"CON", "_CON"},
		{"instance", "_instance"},
		{"1.21.1", "1.21.1"},
		{strings.Repeat("整合包", 60), strings.Repeat("整合包", 60)},
	} {
		got, err := reservePackDirectory(root, tc.title)
		if err != nil || got != filepath.Join(root, tc.want) {
			t.Fatalf("%q: got %q, want %q, err = %v", tc.title, got, tc.want, err)
		}
	}
}

func TestCanceledPackRemovesItsNamedDirectory(t *testing.T) {
	e := packTestEngine(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	_, err := e.createInstance(ctx, Instance{Version: "test-version"}, "Canceled Pack.zip", func(game string) error {
		if filepath.Base(game) != "Canceled Pack" {
			t.Errorf("pack downloaded into %s", game)
		}
		cancel(ErrTaskCanceled)
		return ctx.Err()
	}, func(string) {})
	if err == nil {
		t.Fatal("canceled import succeeded")
	}
	dirs, err := os.ReadDir(filepath.Join(e.GameRoot, "versions"))
	if err != nil || len(dirs) != 0 {
		t.Fatalf("canceled import left directories %v, err = %v", dirs, err)
	}
}

type curseTestTransport func(*http.Request) (*http.Response, error)

func (f curseTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDefaultFabricAPI(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to download Fabric API")
	}
	game := t.TempDir()
	i := Instance{Version: "1.21.1", Loader: "fabric"}
	for range 2 {
		if err := installFabricAPI(t.Context(), i, game, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}
	mod, err := os.ReadDir(filepath.Join(game, "mods"))
	if err != nil || len(mod) != 1 || !strings.HasPrefix(mod[0].Name(), "fabric-api-") {
		t.Fatalf("expected one Fabric API JAR: %v %v", mod, err)
	}
	t.Log(mod[0].Name())
}

func TestModrinthResources(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to use Modrinth downloads")
	}
	e := Engine{DataDir: t.TempDir()}
	i := Instance{ID: "test", Version: "1.21.1", Loader: "fabric"}
	if err := os.MkdirAll(e.GameDir(i), 0700); err != nil {
		t.Fatal(err)
	}
	world := filepath.Join(e.GameDir(i), "saves", "测试世界")
	if err := os.MkdirAll(world, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ kind, slug, folder string }{
		{"mod", "sodium", "mods"},
		{"shader", "complementary-reimagined", "shaderpacks"},
		{"resourcepack", "fresh-animations", "resourcepacks"},
		{"datapack", "terralith", "saves/测试世界/datapacks"},
	} {
		filter := i
		if target.kind == "shader" {
			filter.Loader = "iris"
		}
		hits, _, err := Search(t.Context(), target.kind, strings.ReplaceAll(target.slug, "-", " "), filter, 0)
		if err != nil || len(hits) == 0 {
			t.Fatalf("search %s: %v", target.kind, err)
		}
		if icon, err := ProjectIcon(t.Context(), hits[0].IconURL); err != nil || len(icon) == 0 {
			t.Fatalf("icon for %s: %v", target.kind, err)
		}
		versions, err := ResourceVersions(t.Context(), target.slug, target.kind, i)
		if err != nil || len(versions) == 0 {
			t.Fatalf("versions for %s: %v", target.slug, err)
		}
		index := slices.IndexFunc(versions, func(v ResourceVersion) bool { return slices.Contains(v.GameVersions, i.Version) })
		if index < 0 {
			t.Fatalf("no compatible %s version", target.kind)
		}
		if target.kind == "datapack" {
			err = e.InstallDatapack(t.Context(), i, "测试世界", versions[index], func(string) {})
		} else {
			err = e.InstallResource(t.Context(), target.kind, i, versions[index], func(string) {})
		}
		if err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(filepath.Join(e.GameDir(i), target.folder))
		if err != nil || len(files) == 0 {
			t.Fatalf("installed %s: %v", target.kind, err)
		}
		t.Logf("installed %s", files[0].Name())
	}
	mods, err := e.Mods(i)
	if err != nil || len(mods) != 1 || !mods[0].Enabled {
		t.Fatalf("mods after install: %v, %v", mods, err)
	}
	copy := Instance{ID: "dragged", Version: i.Version, Loader: i.Loader}
	source := filepath.Join(e.GameDir(i), "mods", mods[0].Filename)
	if err := e.InstallLocalMods(t.Context(), copy, []string{source}); err != nil {
		t.Fatal(err)
	}
	if installed, err := e.Mods(copy); err != nil || len(installed) != 1 || installed[0].ID != "sodium" {
		t.Fatalf("dragged sodium copy: %+v, %v", installed, err)
	}
	if err := e.InstallLocalMods(t.Context(), copy, []string{source}); err == nil {
		t.Fatal("reimport should not overwrite an existing mod")
	}
	if err := e.ToggleMod(i, mods[0]); err != nil {
		t.Fatal(err)
	}
	if mods, err = e.Mods(i); err != nil || len(mods) != 1 || mods[0].Enabled {
		t.Fatalf("mods after toggle: %v, %v", mods, err)
	}
	if err := e.RemoveMod(i, mods[0]); err != nil {
		t.Fatal(err)
	}
	if mods, err = e.Mods(i); err != nil || len(mods) != 0 {
		t.Fatalf("mods after removal: %v, %v", mods, err)
	}
}

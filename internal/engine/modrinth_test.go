package engine

import (
	"archive/zip"
	"bytes"
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
	z, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := installPackFiles(t.Context(), root, packIndex{}, z.File, func(string) {}); err != nil {
		t.Fatal(err)
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
	e := Engine{DataDir: t.TempDir(), GameRoot: filepath.Join(t.TempDir(), ".minecraft")}
	version := "test-version"
	base := filepath.Join(e.GameRoot, "versions", version)
	for name, content := range map[string]string{
		filepath.Join(base, version+".json"):                               `{"id":"test-version","mainClass":"Main","downloads":{"client":{"url":"https://invalid.example/client.jar"}},"assetIndex":{"id":"test-assets","url":"https://invalid.example/index.json"}}`,
		filepath.Join(base, version+".jar"):                                "cached client",
		filepath.Join(e.GameRoot, "assets", "indexes", "test-assets.json"): "{}",
		filepath.Join(e.javaHome(8), "bin", "java.exe"):                    "cached java",
		filepath.Join(e.javaHome(8), "bin", "javac.exe"):                   "cached javac",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
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

	first, err := e.ImportPack(t.Context(), path, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.ImportPack(t.Context(), path, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Name != "Fixture pack" || first.Java != 8 {
		t.Fatalf("pack instances were not isolated: %+v, %+v", first, second)
	}
	for _, instance := range []Instance{first, second} {
		if e.GameDir(instance) != filepath.Join(e.GameRoot, "versions", instance.ID) ||
			!regularFile(filepath.Join(e.GameDir(instance), instance.ID+".jar")) ||
			!regularFile(filepath.Join(e.GameDir(instance), instance.ID+".json")) {
			t.Fatalf("incorrect portable layout: %s", e.GameDir(instance))
		}
		content, err := os.ReadFile(filepath.Join(e.GameDir(instance), "mods", "example.jar"))
		if err != nil || string(content) != "pack mod" {
			t.Fatalf("pack contents: %q (%v)", content, err)
		}
	}
}

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

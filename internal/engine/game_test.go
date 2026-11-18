package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultChineseOptions(t *testing.T) {
	game := t.TempDir()
	indexPath, options := filepath.Join(game, "index.json"), filepath.Join(game, "options.txt")
	for _, tc := range []struct{ asset, before, want string }{
		{"minecraft/lang/zh_cn.json", "", "lang:zh_cn\n"},
		{"lang/zh_CN.lang", "renderDistance:8\r\nlang:en_US\r\nmusic:0\r\n", "renderDistance:8\r\nmusic:0\r\nlang:zh_CN\r\n"},
	} {
		if err := os.WriteFile(indexPath, []byte(`{"objects":{"`+tc.asset+`":{}}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(options, []byte(tc.before), 0600); err != nil {
			t.Fatal(err)
		}
		if err := defaultChinese(game, indexPath); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(options); err != nil || string(got) != tc.want {
			t.Fatalf("options: %q: %v", got, err)
		}
	}
}

func TestDefaultInstanceName(t *testing.T) {
	for _, args := range []struct{ loader, build, name string }{
		{"", "", "26.3"},
		{"fabric", "0.20.1", "26.3-fabric_0.20.1"},
		{"fabric", "0.20.2", "26.3-fabric_0.20.2"},
	} {
		instance := Instance{Version: "26.3", Loader: args.loader, Build: args.build}
		if name := instance.DefaultName(); name != args.name {
			t.Fatalf("default name = %q, want %q", name, args.name)
		}
	}
}

func TestRemoveInstanceCleansVersionAfterLastUse(t *testing.T) {
	e := Engine{DataDir: t.TempDir(), GameRoot: t.TempDir()}
	first := Instance{ID: "1.21.1-Vanilla", Version: "1.21.1"}
	second := Instance{ID: "1.21.1-fabric_0.18.1", Version: first.Version}
	for _, instance := range []Instance{first, second} {
		if err := os.MkdirAll(e.GameDir(instance), 0700); err != nil {
			t.Fatal(err)
		}
		if err := writeJSON(filepath.Join(e.GameDir(instance), "instance.json"), instance); err != nil {
			t.Fatal(err)
		}
	}
	profilePath := e.baseVersionFile(first.Version, ".json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(profilePath, gameProfile{ID: first.Version}); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveInstance(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.GameDir(first)); !os.IsNotExist(err) || !regularFile(profilePath) {
		t.Fatal("first removal must delete its directory while retaining the shared version")
	}
	if err := e.RemoveInstance(second); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{e.GameDir(second), filepath.Dir(profilePath)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed: %v", dir, err)
		}
	}
}

func TestCanceledInstallRemovesUnusedVersion(t *testing.T) {
	e := Engine{DataDir: t.TempDir(), GameRoot: t.TempDir()}
	instance := Instance{Version: "26.3"}
	profilePath := e.baseVersionFile(instance.Version, ".json")
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		t.Fatal(err)
	}
	profile := gameProfile{ID: instance.Version, MainClass: "Main"}
	profile.Downloads.Client.URL = "https://invalid.example/client.jar"
	if err := writeJSON(profilePath, profile); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	if _, err := e.Install(ctx, instance, func(string) { cancel(ErrTaskCanceled) }); err == nil {
		t.Fatal("canceled installation should fail")
	}
	if entries, err := os.ReadDir(filepath.Dir(filepath.Dir(profilePath))); err != nil || len(entries) != 0 {
		t.Fatalf("canceled installation left version or staging directories: %v (%v)", entries, err)
	}
}

func TestMinecraftLaunch(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to use the real game and network")
	}
	e := Engine{DataDir: t.TempDir()}
	loader := os.Getenv("HJML_TEST_LOADER")
	if loader == "" {
		loader = "fabric"
	}
	if cache := os.Getenv("HJML_TEST_CACHE"); cache != "" {
		e.DataDir = cache
	}
	e.GameRoot = os.Getenv("HJML_TEST_GAME_ROOT")
	status := func(string) {}
	build := os.Getenv("HJML_TEST_BUILD")
	instance, err := e.Install(t.Context(), Instance{Name: loader + " test", Version: "1.21.1", Loader: loader, Build: build}, status)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Java != 21 || instance.Build == "" {
		t.Fatalf("unexpected instance %+v", instance)
	}
	if build != "" && instance.Build != build {
		t.Fatalf("selected build %s was changed to %s", build, instance.Build)
	}
	if loader == "fabric" {
		mods, err := e.Mods(instance)
		if err != nil || len(mods) != 1 || mods[0].ID != "fabric-api" {
			t.Fatalf("default Fabric API missing: %+v %v", mods, err)
		}
	}
	if installed, err := e.Instances(); err != nil || len(installed) == 0 || installed[0].ID != instance.ID {
		t.Fatalf("instance not saved: %+v, %v", installed, err)
	}
	account, err := LocalAccount("HJML_Test")
	if err != nil {
		t.Fatal(err)
	}
	game, err := e.Launch(t.Context(), instance, account, 2048, status)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = game.process.Kill(); <-game.Done })
	deadline := time.After(90 * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-game.Done:
			log, _ := os.ReadFile(game.LogPath)
			t.Fatalf("game exited: %v\n%s", err, log)
		case <-deadline:
			log, _ := os.ReadFile(game.LogPath)
			t.Fatalf("game did not finish startup\n%s", log)
		case <-ticker.C:
			log, _ := os.ReadFile(game.LogPath)
			if strings.Contains(string(log), "Created:") && strings.Contains(string(log), "Setting user: HJML_Test") {
				t.Logf("Minecraft + %s %s created its textures and initialized the local player", loader, instance.Build)
				return
			}
		}
	}
}

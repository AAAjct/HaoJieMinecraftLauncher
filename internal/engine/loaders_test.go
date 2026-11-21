package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	if err := downloadForgeInstaller(t.Context(), "https://maven.neoforged.net/releases"+path, target, "下载 NeoForge 安装器", func(string) {}); err != nil {
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

func TestForgeFamilyCachesInstallersUnderLibraries(t *testing.T) {
	const jar = "installer fixture"
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		body := jar
		if strings.HasSuffix(req.URL.Path, ".sha1") {
			body = fmt.Sprintf("%x", sha1.Sum([]byte(jar)))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	for _, test := range []struct {
		instance Instance
		path     string
	}{
		{Instance{Loader: "forge", Version: "1.21.1", Build: "52.1.20"}, "net/minecraftforge/forge/1.21.1-52.1.20/forge-1.21.1-52.1.20-installer.jar"},
		{Instance{Loader: "neoforge", Version: "1.21.1", Build: "21.1.264"}, "net/neoforged/neoforge/21.1.264/neoforge-21.1.264-installer.jar"},
		{Instance{Loader: "neoforge", Version: "1.20.1", Build: "47.1.106"}, "net/neoforged/neoforge/47.1.106/forge-1.20.1-47.1.106-installer.jar"},
	} {
		t.Run(test.instance.Loader+"-"+test.instance.Version, func(t *testing.T) {
			e := Engine{GameRoot: t.TempDir()}
			installer, err := e.downloadForgeFamilyInstaller(e.resumableDownloads(t.Context()), test.instance, test.instance.Loader, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.installForgeFamily(t.Context(), test.instance, gameProfile{}, test.instance.Loader, installer, "", func(string) {}); err == nil {
				t.Fatal("non-ZIP fixture was accepted as an installer")
			}
			target := filepath.Join(e.GameRoot, "libraries", filepath.FromSlash(test.path))
			if got, err := os.ReadFile(target); err != nil || string(got) != jar {
				t.Fatalf("installer at %s = %q, err = %v", target, got, err)
			}
			if _, err := os.Stat(filepath.Join(e.GameRoot, test.instance.Loader)); !os.IsNotExist(err) {
				t.Fatalf("unexpected loader directory in game root: %v", err)
			}
		})
	}
}

func TestNeoForgeDownloadsClientInstallerAndJDKConcurrently(t *testing.T) {
	const clientJar, installerJar = "Minecraft fixture", "invalid NeoForge ZIP"
	const packageID = "00000000-0000-0000-0000-000000000021"
	const jdkFile = "zulu21-win_x64.zip"
	const jdkURL = "https://cdn.azul.com/" + jdkFile
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for path, contents := range map[string]string{
		"zulu/bin/java.exe": "java", "zulu/bin/javac.exe": "javac", "zulu/release": "JAVA_VERSION=21.0.1",
	} {
		entry, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	begun := make(chan struct{})
	var downloads atomic.Int32
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		var body string
		var download bool
		switch {
		case req.URL.Path == "/version/1.21.1/client":
			body, download = clientJar, true
		case strings.HasSuffix(req.URL.Path, "-installer.jar.sha1"):
			body = fmt.Sprintf("%x", sha1.Sum([]byte(installerJar)))
		case strings.HasSuffix(req.URL.Path, "-installer.jar"):
			body, download = installerJar, true
		case req.URL.Query().Get("java_version") == "21":
			body = fmt.Sprintf(`[{"package_uuid":%q,"name":%q,"java_version":[21,0,1]}]`, packageID, jdkFile)
		case req.URL.String() == azulAPI+packageID:
			body = fmt.Sprintf(`{"download_url":%q,"sha256_hash":"%x"}`, jdkURL, sha256.Sum256(archive.Bytes()))
		case req.URL.String() == jdkURL:
			body, download = archive.String(), true
		default:
			return nil, fmt.Errorf("unexpected request %s", req.URL)
		}
		if download {
			if downloads.Add(1) == 3 {
				close(begun)
			}
			select {
			case <-begun:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	e := Engine{DataDir: t.TempDir(), GameRoot: t.TempDir()}
	i := Instance{Version: "1.21.1", Loader: "neoforge", Build: "21.1.264"}
	profile := gameProfile{ID: i.Version, MainClass: "Main"}
	profile.JavaVersion.MajorVersion = 21
	profile.Downloads.Client = artifact{URL: "https://piston-data.mojang.com/client.jar", Size: int64(len(clientJar)), SHA1: fmt.Sprintf("%x", sha1.Sum([]byte(clientJar)))}
	if err := os.MkdirAll(e.versionCacheDir(i.Version), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(e.baseVersionFile(i.Version, ".json"), profile); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var progressMu sync.Mutex
	var planned []string
	ctx = WithStageProgress(ctx, func(updates []StageProgress) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if planned == nil {
			for _, update := range updates {
				planned = append(planned, update.Label)
				if update.Fraction >= 0 {
					t.Errorf("initial stage is already active: %+v", update)
				}
			}
			return
		}
		for _, update := range updates {
			if !slices.Contains(planned, update.Label) {
				t.Errorf("unplanned stage appeared during download: %s", update.Label)
			}
		}
	})
	_, err := e.Install(ctx, i, func(string) {})
	if !errors.Is(err, zip.ErrFormat) || downloads.Load() != 3 || !isJDK(e.javaHome(21), 21) || !regularFile(e.baseVersionFile(i.Version, ".jar")) {
		t.Fatalf("expected all three downloads before installer validation, got %d requests and %v", downloads.Load(), err)
	}
	want := []string{
		"读取原版版本信息", "下载 Minecraft 客户端", "下载并安装 JDK", "下载 NeoForge 安装器",
		"分析加载器运行库", "下载 NeoForge 运行库", "安装 NeoForge", "下载游戏运行库",
		"下载 Minecraft 资源索引", "下载 Minecraft 资源", "安装游戏实例",
	}
	if !slices.Equal(planned, want) {
		t.Fatalf("initial install stages = %v, want %v", planned, want)
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

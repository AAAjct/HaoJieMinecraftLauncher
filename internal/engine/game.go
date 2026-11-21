package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const manifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type Version struct {
	ID          string
	Type        string
	URL         string
	SHA1        string
	ReleaseTime time.Time
}

func Versions(ctx context.Context) ([]Version, error) {
	source := func(url string) catalogSource[[]Version] {
		return func(ctx context.Context) ([]Version, error) {
			var manifest struct{ Versions []Version }
			if err := catalogJSON(ctx, url, &manifest); err != nil {
				return nil, err
			}
			if len(manifest.Versions) == 0 {
				return nil, fmt.Errorf("版本目录为空")
			}
			return manifest.Versions, nil
		}
	}
	return preferCatalog(ctx, source(bmcl+"/mc/game/version_manifest_v2.json"), source(manifestURL))
}

type restriction struct {
	Action string `json:"action"`
	OS     struct {
		Name    string `json:"name,omitempty"`
		Arch    string `json:"arch,omitempty"`
		Version string `json:"version,omitempty"`
	} `json:"os,omitzero"`
	Features map[string]bool `json:"features,omitempty"`
}

var windowsRelease = func() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}()

func permitted(rules []restriction, features map[string]bool) bool {
	if len(rules) == 0 {
		return true
	}
	allowed := false
	for _, rule := range rules {
		if rule.OS.Name != "" && rule.OS.Name != "windows" {
			continue
		}
		if rule.OS.Arch != "" {
			amd64, _ := regexp.MatchString(rule.OS.Arch, "amd64")
			x64, _ := regexp.MatchString(rule.OS.Arch, "x86_64")
			if !amd64 && !x64 {
				continue
			}
		}
		if rule.OS.Version != "" {
			if ok, _ := regexp.MatchString(rule.OS.Version, windowsRelease); !ok {
				continue
			}
		}
		match := true
		for key, required := range rule.Features {
			if features[key] != required {
				match = false
				break
			}
		}
		if match {
			allowed = rule.Action == "allow"
		}
	}
	return allowed
}

type library struct {
	Name      string            `json:"name"`
	URL       string            `json:"url,omitempty"`
	SHA1      string            `json:"sha1,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Size      int64             `json:"size,omitempty"`
	Rules     []restriction     `json:"rules,omitempty"`
	Natives   map[string]string `json:"natives,omitempty"`
	Downloads struct {
		Artifact    artifact            `json:"artifact,omitzero"`
		Classifiers map[string]artifact `json:"classifiers,omitempty"`
	} `json:"downloads,omitzero"`
}

type gameProfile struct {
	ID                 string `json:"id"`
	InheritsFrom       string `json:"inheritsFrom,omitempty"`
	MainClass          string `json:"mainClass"`
	Type               string `json:"type,omitempty"`
	LaunchID           string `json:"-"`
	Assets             string `json:"assets,omitempty"`
	MinecraftArguments string `json:"minecraftArguments,omitempty"`
	JavaVersion        struct {
		MajorVersion int    `json:"majorVersion"`
		Component    string `json:"component,omitempty"`
	} `json:"javaVersion,omitzero"`
	Downloads struct {
		Client artifact `json:"client,omitzero"`
	} `json:"downloads,omitzero"`
	AssetIndex struct {
		ID string `json:"id"`
		artifact
	} `json:"assetIndex,omitzero"`
	Arguments struct {
		Game []json.RawMessage `json:"game"`
		JVM  []json.RawMessage `json:"jvm"`
	} `json:"arguments,omitzero"`
	Libraries []library `json:"libraries,omitempty"`
	Logging   struct {
		Client struct {
			Argument string `json:"argument"`
			Type     string `json:"type,omitempty"`
			File     struct {
				ID string `json:"id"`
				artifact
			} `json:"file"`
		} `json:"client,omitzero"`
	} `json:"logging,omitzero"`
}

func (e Engine) baseProfile(ctx context.Context, version string) (gameProfile, error) {
	var profile gameProfile
	if !segment(version) {
		return profile, fmt.Errorf("无效 Minecraft 版本")
	}
	target := e.baseVersionFile(version, ".json")
	if readJSON(target, &profile) != nil || profile.ID != version {
		if err := e.moveLegacyVersionCache(version); err != nil {
			return profile, err
		}
		if readJSON(target, &profile) == nil && profile.ID == version {
			return validatedBaseProfile(profile, version)
		}
		versions, _ := e.CachedVersions()
		var err error
		if !slices.ContainsFunc(versions, func(entry Version) bool { return entry.ID == version }) {
			versions, err = e.RefreshVersions(ctx)
		}
		if err != nil {
			return profile, err
		}
		found := false
		for _, entry := range versions {
			if entry.ID != version {
				continue
			}
			file := withMinecraftMirror(artifact{URL: entry.URL, SHA1: entry.SHA1}, minecraftMirror(entry.URL))
			if err := (transfer{file, target}).copyProfile(ctx); err != nil {
				return profile, err
			}
			found = true
			break
		}
		if !found {
			return profile, fmt.Errorf("Mojang 清单中没有版本 %s", version)
		}
		if err := readJSON(target, &profile); err != nil {
			return profile, err
		}
	}
	return validatedBaseProfile(profile, version)
}

func validatedBaseProfile(profile gameProfile, version string) (gameProfile, error) {
	if profile.ID != version || profile.MainClass == "" || profile.Downloads.Client.URL == "" {
		return profile, fmt.Errorf("版本 %s 没有可用客户端", version)
	}
	major, err := launcherJavaMajor(profile.JavaVersion.MajorVersion)
	profile.JavaVersion.MajorVersion = major
	return profile, err
}

func (t transfer) copyProfile(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(t.target), 0700); err != nil {
		return err
	}
	var err error
	for _, source := range t.orderedSources() {
		t.URL = source
		if err = t.copy(ctx); err == nil {
			return nil
		}
	}
	return err
}

func loaderURL(loader string) string {
	switch loader {
	case "fabric":
		return "https://meta.fabricmc.net/v2/versions/loader/"
	case "quilt":
		return "https://meta.quiltmc.org/v3/versions/loader/"
	default:
		return ""
	}
}

func nextLoader(ctx context.Context, version, loader string) (string, error) {
	if loader == "forge" {
		return recommendedForge(ctx, version)
	}
	versions, err := LoaderBuilds(ctx, version, loader)
	if err != nil {
		return "", err
	}
	for _, build := range versions {
		if build.Stable {
			return build.Version, nil
		}
	}
	if len(versions) > 0 {
		return versions[0].Version, nil
	}
	return "", fmt.Errorf("%s 尚未支持 Minecraft %s", loader, version)
}

const (
	versionInfoStage = "读取原版版本信息"
	clientStage      = "下载 Minecraft 客户端"
	javaStage        = "下载并安装 JDK"
	assetIndexStage  = "下载 Minecraft 资源索引"
	assetsStage      = "下载 Minecraft 资源"
	librariesStage   = "下载游戏运行库"
)

func forgeFamilyName(loader string) string {
	switch loader {
	case "forge":
		return "Forge"
	case "neoforge":
		return "NeoForge"
	}
	return ""
}

func loaderStages(loader string) []string {
	if name := forgeFamilyName(loader); name != "" {
		return []string{"下载 " + name + " 安装器", "分析加载器运行库", "下载 " + name + " 运行库", "安装 " + name}
	}
	if loader != "" {
		return []string{"读取 " + loader + " 加载器信息"}
	}
	return nil
}

func (e Engine) prepareGame(ctx context.Context, instance Instance, minimumJava int, status func(string)) (gameProfile, string, error) {
	status("正在读取版本信息…")
	var base gameProfile
	if err := trackStage(ctx, versionInfoStage, func(ctx context.Context) error {
		var err error
		base, err = e.baseProfile(ctx, instance.Version)
		return err
	}); err != nil {
		return base, "", err
	}
	source := loaderURL(instance.Loader)
	name := forgeFamilyName(instance.Loader)
	usesInstaller := name != ""
	if instance.Loader != "" && ((source == "" && !usesInstaller) || !segment(instance.Build)) {
		return base, "", fmt.Errorf("无效或不受支持的加载器")
	}
	cache := filepath.Join(e.versionCacheDir(instance.Version), instance.Loader+"-"+instance.Build+".json")
	var overlay gameProfile
	needsOverlay := instance.Loader != "" && readJSON(cache, &overlay) != nil
	gameJava := max(base.JavaVersion.MajorVersion, minimumJava)
	var java, installer string
	jar := transfer{minecraftClient(base), e.baseVersionFile(base.ID, ".jar")}
	jobs := []func(context.Context, func(string)) error{
		func(ctx context.Context, status func(string)) error {
			if jar.ready() {
				reportStage(ctx, StageProgress{Label: clientStage, Fraction: 1})
				return nil
			}
			return ensureAll(ctx, []transfer{jar}, clientStage, status)
		},
		func(ctx context.Context, status func(string)) error {
			return trackStage(ctx, javaStage, func(ctx context.Context) error {
				var err error
				java, err = e.EnsureJava(ctx, gameJava, status)
				return err
			})
		},
	}
	loaderSteps := loaderStages(instance.Loader)
	if needsOverlay {
		label := loaderSteps[0]
		jobs = append(jobs, func(ctx context.Context, status func(string)) error {
			return trackStage(ctx, label, func(ctx context.Context) error {
				if usesInstaller {
					var err error
					installer, err = e.downloadForgeFamilyInstaller(ctx, instance, name, status)
					return err
				}
				status("正在" + label + "…")
				addr := source + url.PathEscape(instance.Version) + "/" + url.PathEscape(instance.Build) + "/profile/json"
				if instance.Loader == "fabric" {
					return mirroredJSON(ctx, bmcl+"/fabric-meta/v2/versions/loader/"+strings.TrimPrefix(addr, loaderURL("fabric")), addr, &overlay)
				}
				return fetchJSON(ctx, addr, &overlay)
			})
		})
	} else {
		for _, label := range loaderSteps {
			reportStage(ctx, StageProgress{Label: label, Fraction: 1})
		}
	}
	if err := runParallel(ctx, status, jobs...); err != nil {
		return base, "", err
	}
	if instance.Loader == "" {
		return base, java, nil
	}
	if needsOverlay {
		if usesInstaller {
			var err error
			installerJava := java
			if gameJava != base.JavaVersion.MajorVersion {
				installerJava, err = e.EnsureJava(ctx, base.JavaVersion.MajorVersion, status)
				if err != nil {
					return base, "", err
				}
			}
			overlay, err = e.installForgeFamily(ctx, instance, base, name, installer, installerJava, status)
			if err != nil {
				return base, "", err
			}
		}
		if overlay.InheritsFrom != base.ID || overlay.MainClass == "" {
			return base, "", fmt.Errorf("加载器版本描述无效")
		}
		if err := writeJSON(cache, overlay); err != nil {
			return base, "", err
		}
	}
	base.MainClass = overlay.MainClass
	base.LaunchID = overlay.ID
	base.Libraries = mergeLibraries(base.Libraries, overlay.Libraries)
	base.Arguments.JVM = append(base.Arguments.JVM, overlay.Arguments.JVM...)
	base.Arguments.Game = append(base.Arguments.Game, overlay.Arguments.Game...)
	return base, java, nil
}

func mergeLibraries(base, overlay []library) []library {
	type key struct{ group, name, classifier string }
	coords := func(name string) key {
		parts := strings.Split(name, ":")
		if len(parts) < 3 {
			return key{name: name}
		}
		k := key{group: parts[0], name: parts[1]}
		if len(parts) == 4 {
			k.classifier = parts[3]
		}
		return k
	}
	replaced := map[key]bool{}
	for _, lib := range overlay {
		replaced[coords(lib.Name)] = true
	}
	result := append([]library(nil), overlay...)
	for _, lib := range base {
		if !replaced[coords(lib.Name)] {
			result = append(result, lib)
		}
	}
	return result
}

func mavenArtifact(lib library) (artifact, error) {
	parts := strings.Split(lib.Name, ":")
	if len(parts) < 3 || len(parts) > 4 || !segment(parts[1]) || !segment(parts[2]) {
		return artifact{}, fmt.Errorf("无效 Maven 坐标 %s", lib.Name)
	}
	name := parts[1] + "-" + parts[2]
	if len(parts) == 4 {
		name += "-" + parts[3]
	}
	path := strings.ReplaceAll(parts[0], ".", "/") + "/" + parts[1] + "/" + parts[2] + "/" + name + ".jar"
	repo := lib.URL
	if repo == "" {
		repo = "https://libraries.minecraft.net/"
	}
	return artifact{URL: strings.TrimRight(repo, "/") + "/" + path, Path: path,
		Size: lib.Size, SHA1: lib.SHA1, SHA256: lib.SHA256}, nil
}

func (e Engine) gameTransfers(profile gameProfile) (packages []transfer, classpath, natives []string, err error) {
	root := filepath.Join(e.gameRoot(), "libraries")
	for _, lib := range profile.Libraries {
		if !permitted(lib.Rules, nil) {
			continue
		}
		file := lib.Downloads.Artifact
		if file.URL == "" && file.Path == "" && lib.Natives == nil {
			file, err = mavenArtifact(lib)
			if err != nil {
				return
			}
		}
		if file.Path != "" {
			target, pathErr := localPath(root, file.Path)
			if pathErr != nil {
				err = pathErr
				return
			}
			item := transfer{withMinecraftMirror(file, minecraftMirror(file.URL)), target}
			if file.URL == "" && !item.ready() {
				err = fmt.Errorf("缺少安装器生成的运行库 %s", lib.Name)
				return
			}
			packages = append(packages, item)
			classpath = append(classpath, target)
		}
		if classifier := lib.Natives["windows"]; classifier != "" {
			classifier = strings.ReplaceAll(classifier, "${arch}", "64")
			native := lib.Downloads.Classifiers[classifier]
			target, pathErr := localPath(root, native.Path)
			if pathErr != nil || native.URL == "" {
				err = fmt.Errorf("库 %s 缺少 Windows 原生文件", lib.Name)
				return
			}
			packages = append(packages, transfer{withMinecraftMirror(native, minecraftMirror(native.URL)), target})
			natives = append(natives, target)
		}
	}
	jar := e.baseVersionFile(profile.ID, ".jar")
	packages = append(packages, transfer{minecraftClient(profile), jar})
	classpath = append(classpath, jar)
	if file := profile.Logging.Client.File; segment(file.ID) && file.URL != "" {
		packages = append(packages, transfer{withMinecraftMirror(file.artifact, minecraftMirror(file.URL)), filepath.Join(e.gameRoot(), "assets", "log_configs", file.ID)})
	}
	return
}

func minecraftClient(profile gameProfile) artifact {
	return withMinecraftMirror(profile.Downloads.Client, bmcl+"/version/"+url.PathEscape(profile.ID)+"/client")
}

type assetIndex struct {
	Objects map[string]struct {
		Hash string
		Size int64
	}
	Virtual        bool
	MapToResources bool `json:"map_to_resources"`
}

func (e Engine) installGame(ctx context.Context, instance Instance, status func(string)) (gameProfile, error) {
	profile, _, err := e.prepareGame(ctx, instance, 0, status)
	if err != nil {
		return profile, err
	}
	packages, _, _, err := e.gameTransfers(profile)
	if err != nil {
		return profile, err
	}
	return profile, runParallel(ctx, status, func(ctx context.Context, status func(string)) error {
		return ensureAll(ctx, packages, librariesStage, status)
	}, func(ctx context.Context, status func(string)) error {
		return e.downloadAssets(ctx, profile, status)
	})
}

func (e Engine) downloadAssets(ctx context.Context, profile gameProfile, status func(string)) error {
	assets, err := e.assetTransfers(ctx, profile, status)
	if err != nil {
		return err
	}
	// Assets are shared across installations, so finish the other downloads
	// even if a file has exhausted its sources. A retry then only needs the gaps.
	return ensureBatch(ctx, assets, assetsStage, status, false)
}

func (e Engine) assetTransfers(ctx context.Context, profile gameProfile, status func(string)) ([]transfer, error) {
	if !segment(profile.AssetIndex.ID) || profile.AssetIndex.URL == "" {
		return nil, fmt.Errorf("版本缺少资源索引")
	}
	assets := filepath.Join(e.gameRoot(), "assets")
	indexPath := filepath.Join(assets, "indexes", profile.AssetIndex.ID+".json")
	indexFile := transfer{withMinecraftMirror(profile.AssetIndex.artifact, minecraftMirror(profile.AssetIndex.URL)), indexPath}
	if err := ensureAll(ctx, []transfer{indexFile}, assetIndexStage, status); err != nil {
		return nil, err
	}
	var index assetIndex
	if err := readJSON(indexPath, &index); err != nil {
		return nil, err
	}
	files := make([]transfer, 0, len(index.Objects))
	seen := map[string]bool{}
	for _, entry := range index.Objects {
		if seen[entry.Hash] {
			continue
		}
		if !validAssetHash(entry.Hash) {
			return nil, fmt.Errorf("Mojang 资源索引包含无效哈希")
		}
		seen[entry.Hash] = true
		relative := entry.Hash[:2] + "/" + entry.Hash
		files = append(files, transfer{
			artifact: artifact{
				URL: "https://resources.download.minecraft.net/" + relative, Size: entry.Size, SHA1: entry.Hash,
				Mirrors: []string{bmcl + "/assets/" + relative},
			},
			target: filepath.Join(assets, "objects", filepath.FromSlash(relative)),
		})
	}
	return files, nil
}

var assetHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validAssetHash(s string) bool { return assetHash.MatchString(s) }

func (e Engine) versionCacheDir(version string) string {
	if e.GameRoot == "" {
		return filepath.Join(e.gameRoot(), "versions", version)
	}
	return filepath.Join(e.cacheDir(), "versions", version)
}

func (e Engine) moveLegacyVersionCache(version string) error {
	if e.GameRoot == "" {
		return nil
	}
	instancePublishMu.Lock()
	defer instancePublishMu.Unlock()
	target := e.versionCacheDir(version)
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		return err
	}
	old := filepath.Join(e.GameRoot, "versions", version)
	info, err := os.Lstat(old)
	if os.IsNotExist(err) || err == nil && !info.IsDir() {
		return nil
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(old)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if name == version+".jar" || name == version+".json" {
			continue
		}
		loader, _, ok := strings.Cut(name, "-")
		if !ok || !strings.HasSuffix(name, ".json") || (loader != "forge" && loader != "neoforge" && loaderURL(loader) == "") {
			return nil
		}
		var overlay gameProfile
		if readJSON(filepath.Join(old, name), &overlay) != nil || overlay.InheritsFrom != version {
			return nil
		}
	}
	var base gameProfile
	if readJSON(filepath.Join(old, version+".json"), &base) != nil || base.ID != version ||
		base.MainClass == "" || base.Downloads.Client.URL == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	return os.Rename(old, target)
}

func (e Engine) baseVersionFile(version, extension string) string {
	name := version + extension
	if e.GameRoot == "" {
		name = "version.json"
		if extension == ".jar" {
			name = "client.jar"
		}
	}
	return filepath.Join(e.versionCacheDir(version), name)
}

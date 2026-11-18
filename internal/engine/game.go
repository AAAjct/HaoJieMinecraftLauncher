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
	return firstCatalog(ctx, source(manifestURL), source(bmcl+"/mc/game/version_manifest_v2.json"))
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
			if err := (transfer{artifact{URL: entry.URL, SHA1: entry.SHA1}, target}).copyProfile(ctx); err != nil {
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
	if profile.ID != version || profile.MainClass == "" || profile.Downloads.Client.URL == "" {
		return profile, fmt.Errorf("版本 %s 没有可用客户端", version)
	}
	if profile.JavaVersion.MajorVersion == 0 {
		profile.JavaVersion.MajorVersion = 8
	}
	return profile, nil
}

func (t transfer) copyProfile(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(t.target), 0700); err != nil {
		return err
	}
	return t.copy(ctx)
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

func (e Engine) profile(ctx context.Context, instance Instance, status func(string)) (gameProfile, error) {
	base, err := e.baseProfile(ctx, instance.Version)
	if err != nil || instance.Loader == "" {
		return base, err
	}
	source := loaderURL(instance.Loader)
	if (source == "" && instance.Loader != "forge") || !segment(instance.Build) {
		return base, fmt.Errorf("无效或不受支持的加载器")
	}
	cache := filepath.Join(e.gameRoot(), "versions", instance.Version, instance.Loader+"-"+instance.Build+".json")
	var overlay gameProfile
	if readJSON(cache, &overlay) != nil {
		if instance.Loader == "forge" {
			overlay, err = e.installForge(ctx, instance, base, status)
		} else {
			addr := source + url.PathEscape(instance.Version) + "/" + url.PathEscape(instance.Build) + "/profile/json"
			err = fetchJSON(ctx, addr, &overlay)
		}
		if err != nil {
			return base, err
		}
		if overlay.InheritsFrom != base.ID || overlay.MainClass == "" {
			return base, fmt.Errorf("加载器版本描述无效")
		}
		if err := writeJSON(cache, overlay); err != nil {
			return base, err
		}
	}
	base.MainClass = overlay.MainClass
	base.LaunchID = overlay.ID
	base.Libraries = mergeLibraries(base.Libraries, overlay.Libraries)
	base.Arguments.JVM = append(base.Arguments.JVM, overlay.Arguments.JVM...)
	base.Arguments.Game = append(base.Arguments.Game, overlay.Arguments.Game...)
	return base, nil
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
			item := transfer{file, target}
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
			packages = append(packages, transfer{native, target})
			natives = append(natives, target)
		}
	}
	jar := e.baseVersionFile(profile.ID, ".jar")
	packages = append(packages, transfer{profile.Downloads.Client, jar})
	classpath = append(classpath, jar)
	if file := profile.Logging.Client.File; segment(file.ID) && file.URL != "" {
		packages = append(packages, transfer{file.artifact, filepath.Join(e.gameRoot(), "assets", "log_configs", file.ID)})
	}
	return
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
	status("正在读取版本信息…")
	profile, err := e.profile(ctx, instance, status)
	if err != nil {
		return profile, err
	}
	packages, _, _, err := e.gameTransfers(profile)
	if err != nil {
		return profile, err
	}
	if err := ensureAll(ctx, packages, "下载 Minecraft 和运行库", status); err != nil {
		return profile, err
	}
	return profile, e.ensureAssets(ctx, profile, status)
}

func (e Engine) ensureAssets(ctx context.Context, profile gameProfile, status func(string)) error {
	if !segment(profile.AssetIndex.ID) || profile.AssetIndex.URL == "" {
		return fmt.Errorf("版本缺少资源索引")
	}
	assets := filepath.Join(e.gameRoot(), "assets")
	indexPath := filepath.Join(assets, "indexes", profile.AssetIndex.ID+".json")
	if err := (transfer{profile.AssetIndex.artifact, indexPath}).ensure(ctx); err != nil {
		return err
	}
	var index assetIndex
	if err := readJSON(indexPath, &index); err != nil {
		return err
	}
	files := make([]transfer, 0, len(index.Objects))
	seen := map[string]bool{}
	for _, entry := range index.Objects {
		if seen[entry.Hash] {
			continue
		}
		if !validAssetHash(entry.Hash) {
			return fmt.Errorf("Mojang 资源索引包含无效哈希")
		}
		seen[entry.Hash] = true
		relative := entry.Hash[:2] + "/" + entry.Hash
		files = append(files, transfer{
			artifact: artifact{URL: "https://resources.download.minecraft.net/" + relative, Size: entry.Size, SHA1: entry.Hash},
			target:   filepath.Join(assets, "objects", filepath.FromSlash(relative)),
		})
	}
	return ensureAll(ctx, files, "检查游戏资源", status)
}

var assetHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validAssetHash(s string) bool { return assetHash.MatchString(s) }

func (e Engine) baseVersionFile(version, extension string) string {
	name := version + extension
	if e.GameRoot == "" {
		name = "version.json"
		if extension == ".jar" {
			name = "client.jar"
		}
	}
	return filepath.Join(e.gameRoot(), "versions", version, name)
}

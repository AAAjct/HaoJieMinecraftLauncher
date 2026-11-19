package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const modrinth = "https://api.modrinth.com/v2/"

func modrinthJSON[T any](ctx context.Context, path string, out *T) error {
	return mirroredJSON(ctx, mcim+"/modrinth/v2/"+path, modrinth+path, out)
}

var resourcePublishMu sync.Mutex

type Project struct {
	ID                string `json:"project_id"`
	Title             string
	Slug              string
	Description       string
	Author            string
	IconURL           string `json:"icon_url"`
	Categories        []string
	DisplayCategories []string `json:"display_categories"`
	Versions          []string
	Downloads         int
	DateModified      time.Time `json:"date_modified"`
}

func ProjectIcon(ctx context.Context, source string) ([]byte, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || !slices.Contains([]string{"cdn.modrinth.com", "media.forgecdn.net", "mediafiles.forgecdn.net", "mediafilez.forgecdn.net", "edge.forgecdn.net"}, u.Host) || u.User != nil {
		return nil, fmt.Errorf("无效的项目图标地址")
	}
	from := func(address string) catalogSource[[]byte] {
		return func(ctx context.Context) ([]byte, error) {
			res, err := get(ctx, address)
			if err != nil {
				return nil, err
			}
			defer res.Body.Close()
			return io.ReadAll(io.LimitReader(res.Body, 2<<20))
		}
	}
	if mirror := resourceMirror(source); mirror != "" {
		return preferCatalog(ctx, from(mirror), from(source))
	}
	return from(source)(ctx)
}

type ResourceVersion struct {
	VersionType   string `json:"version_type"`
	ID            string
	ProjectID     string `json:"project_id"`
	Name          string
	VersionNumber string   `json:"version_number"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string
	Files         []resourceFile
	Dependencies  []resourceDependency
}

type resourceDependency struct {
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
	Type      string `json:"dependency_type"`
}

type resourceFile struct {
	Filename string
	URL      string
	Primary  bool
	Size     int64
	Hashes   struct{ SHA512 string }
	SHA1     string `json:"-"`
}

const ResourcePageSize = 12

func Search(ctx context.Context, kind, query string, filter Instance, page int) ([]Project, int, error) {
	filters := [][]string{{"project_type:" + kind}}
	if filter.Version != "" {
		filters = append(filters, []string{"versions:" + filter.Version})
	}
	if kind == "mod" || kind == "modpack" {
		loaders := modLoaders
		if SupportedModLoader(filter.Loader) {
			loaders = []string{filter.Loader}
		}
		facets := make([]string, 0, len(loaders))
		for _, loader := range loaders {
			facets = append(facets, "categories:"+loader)
		}
		filters = append(filters, facets)
	}
	facets, _ := json.Marshal(filters)
	q := url.Values{"query": {query}, "facets": {string(facets)}, "limit": {strconv.Itoa(ResourcePageSize)}, "offset": {strconv.Itoa(max(0, page) * ResourcePageSize)}}
	var search struct {
		Hits  []Project
		Total int `json:"total_hits"`
	}
	err := modrinthJSON(ctx, "search?"+q.Encode(), &search)
	return search.Hits, search.Total, err
}

func ResourceVersions(ctx context.Context, id, kind string, instance Instance) ([]ResourceVersion, error) {
	if strings.HasPrefix(id, cursePrefix) {
		versions, _, err := CurseForgeVersions(ctx, id, kind, instance, 0)
		return versions, err
	}
	q := url.Values{"include_changelog": {"false"}}
	if kind == "datapack" {
		q.Set("loaders", `["datapack"]`)
	}
	if kind == "mod" {
		if instance.Loader != "" && !SupportedModLoader(instance.Loader) {
			return nil, fmt.Errorf("不提供 %s 加载器筛选", instance.Loader)
		}
		for key, value := range map[string]string{"game_versions": instance.Version, "loaders": instance.Loader} {
			if value != "" {
				data, _ := json.Marshal([]string{value})
				q.Set(key, string(data))
			}
		}
	}
	var versions []ResourceVersion
	err := modrinthJSON(ctx, "project/"+url.PathEscape(id)+"/version?"+q.Encode(), &versions)
	return versions, err
}

func (v ResourceVersion) selectedFile(extension string) (resourceFile, error) {
	var candidate resourceFile
	for _, file := range v.Files {
		if !segment(file.Filename) || (extension != "" && !strings.HasSuffix(strings.ToLower(file.Filename), extension)) {
			continue
		}
		if candidate.Filename == "" || file.Primary {
			candidate = file
		}
		if file.Primary {
			break
		}
	}
	source, err := url.Parse(candidate.URL)
	if err == nil && source.Scheme == "https" && source.User == nil {
		if source.Host == "cdn.modrinth.com" && validSHA512(candidate.Hashes.SHA512) ||
			curseForgeCDN(source.Host) && validAssetHash(candidate.SHA1) {
			return candidate, nil
		}
	}
	if strings.HasPrefix(v.ID, cursePrefix) && candidate.Filename != "" && candidate.URL == "" {
		return resourceFile{}, fmt.Errorf("CurseForge 未提供 %s 的第三方下载地址", candidate.Filename)
	}
	return resourceFile{}, fmt.Errorf("%s 没有可校验的下载文件", v.Name)
}

func validSHA512(s string) bool {
	return len(s) == 128 && strings.Trim(s, "0123456789abcdefABCDEF") == ""
}

func (f resourceFile) transfer(dir string) transfer {
	return transfer{artifact{URL: f.URL, SHA512: f.Hashes.SHA512, SHA1: f.SHA1, Size: f.Size}, filepath.Join(dir, f.Filename)}
}

func resolveMods(ctx context.Context, instance Instance, root ResourceVersion) ([]resourceFile, error) {
	var files []resourceFile
	seen := map[string]string{}
	var visit func(ResourceVersion) error
	visit = func(version ResourceVersion) error {
		if existing := seen[version.ProjectID]; existing != "" {
			if existing != version.ID {
				return fmt.Errorf("模组依赖要求同一项目的不同版本")
			}
			return nil
		}
		if len(seen) >= 100 {
			return fmt.Errorf("模组依赖超过 100 项")
		}
		if !slices.Contains(version.GameVersions, instance.Version) || !slices.Contains(version.Loaders, instance.Loader) {
			return fmt.Errorf("%s 与该实例的 Minecraft 或加载器不兼容", version.Name)
		}
		file, err := version.selectedFile(".jar")
		if err != nil {
			return err
		}
		seen[version.ProjectID] = version.ID
		files = append(files, file)
		for _, dep := range version.Dependencies {
			if dep.Type != "required" {
				continue
			}
			var next ResourceVersion
			switch {
			case dep.VersionID != "":
				if err := modrinthJSON(ctx, "version/"+url.PathEscape(dep.VersionID), &next); err != nil {
					return err
				}
			case dep.ProjectID != "":
				if seen[dep.ProjectID] != "" {
					continue
				}
				all, err := ResourceVersions(ctx, dep.ProjectID, "mod", instance)
				if err != nil {
					return err
				}
				compatible := slices.IndexFunc(all, func(v ResourceVersion) bool {
					return slices.Contains(v.GameVersions, instance.Version) && slices.Contains(v.Loaders, instance.Loader)
				})
				if compatible < 0 {
					return fmt.Errorf("找不到兼容的依赖 %s", dep.ProjectID)
				}
				next = all[compatible]
			default:
				return fmt.Errorf("%s 有无法自动解析的必需依赖", version.Name)
			}
			if err := visit(next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return files, nil
}

func (e Engine) InstallResource(ctx context.Context, kind string, instance Instance, version ResourceVersion, status func(string)) error {
	return e.installResource(ctx, kind, instance, "", version, status)
}

func (e Engine) InstallDatapack(ctx context.Context, instance Instance, world string, version ResourceVersion, status func(string)) error {
	if !slices.Contains(version.Loaders, "datapack") || !slices.Contains(version.GameVersions, instance.Version) {
		return fmt.Errorf("所选数据包与实例版本不兼容")
	}
	return e.installResource(ctx, "datapack", instance, world, version, status)
}

func (e Engine) installResource(ctx context.Context, kind string, instance Instance, world string, version ResourceVersion, status func(string)) error {
	ctx = e.resumableDownloads(ctx)
	var files []resourceFile
	var err error
	var folder string
	switch kind {
	case "mod":
		if !SupportedModLoader(instance.Loader) {
			return fmt.Errorf("请先选择带 Fabric、Forge 或 NeoForge 的实例")
		}
		status("正在解析必需模组依赖…")
		files, err = resolveMods(ctx, instance, version)
		folder = "mods"
	case "shader", "resourcepack", "datapack":
		var file resourceFile
		file, err = version.selectedFile(".zip")
		files = []resourceFile{file}
		folder = kind + "s"
		if kind == "shader" {
			folder = "shaderpacks"
		}
	default:
		return fmt.Errorf("无效资源类型")
	}
	if err != nil {
		return err
	}

	game := e.GameDir(instance)
	if kind == "datapack" {
		worlds, err := e.Worlds(instance)
		if err != nil {
			return err
		}
		if !slices.Contains(worlds, world) {
			return fmt.Errorf("目标存档不存在")
		}
		folder = filepath.Join("saves", world, "datapacks")
	}
	stage, err := makeCacheStage(ctx, ".download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	targetDir := filepath.Join(game, folder)
	work, err := resourceDownloadWork(files, stage, targetDir, kind == "mod")
	if err != nil {
		return err
	}
	if err := ensureAll(ctx, work, "下载资源", status); err != nil {
		return curseDownloadError(err)
	}
	resourcePublishMu.Lock()
	defer resourcePublishMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if kind == "datapack" && !regularFile(filepath.Join(filepath.Dir(targetDir), "level.dat")) {
		return fmt.Errorf("目标存档不存在")
	}
	return publishResources(work, targetDir, kind == "mod")
}

func resourceDownloadWork(files []resourceFile, stage, targetDir string, mod bool) ([]transfer, error) {
	resourcePublishMu.Lock()
	defer resourcePublishMu.Unlock()
	var work []transfer
	names := map[string]bool{}
	for _, file := range files {
		key := strings.ToLower(file.Filename)
		if names[key] {
			return nil, fmt.Errorf("依赖包含同名文件 %s", file.Filename)
		}
		names[key] = true
		found, err := installedResource(file.transfer(targetDir), mod)
		if err != nil {
			return nil, err
		}
		if !found {
			work = append(work, file.transfer(stage))
		}
	}
	return work, nil
}

func installedResource(item transfer, mod bool) (bool, error) {
	name := filepath.Base(item.target)
	paths := []string{item.target}
	if mod {
		paths = append(paths, item.target+".disabled")
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode().IsRegular() && (item.Size <= 0 || info.Size() == item.Size) {
			file, err := os.Open(path)
			if err != nil {
				return false, err
			}
			digest, expected := item.digest()
			_, err = io.Copy(digest, file)
			file.Close()
			if err != nil {
				return false, err
			}
			if strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected) {
				return true, nil
			}
		}
		return false, fmt.Errorf("%s 已存在，请先处理该文件", name)
	}
	return false, nil
}

func publishResources(work []transfer, targetDir string, mod bool) error {
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return err
	}
	var toPublish []transfer
	for _, item := range work {
		target := transfer{item.artifact, filepath.Join(targetDir, filepath.Base(item.target))}
		found, err := installedResource(target, mod)
		if err != nil {
			return err
		}
		if !found {
			toPublish = append(toPublish, item)
		}
	}
	var added []string
	for _, item := range toPublish {
		target := filepath.Join(targetDir, filepath.Base(item.target))
		if err := linkOrCopy(item.target, target); err != nil {
			for _, path := range added {
				_ = os.Remove(path)
			}
			return err
		}
		added = append(added, target)
	}
	return nil
}

type packIndex struct {
	FormatVersion int
	Game, Name    string
	Dependencies  map[string]string
	Files         []struct {
		Path      string
		FileSize  int64
		Downloads []string
		Env       struct{ Client string }
		Hashes    struct{ SHA512 string }
	}
}

func (e Engine) DownloadPack(ctx context.Context, version ResourceVersion, status func(string)) (Instance, error) {
	ctx = e.resumableDownloads(ctx)
	extension := ".mrpack"
	if strings.HasPrefix(version.ID, cursePrefix) {
		extension = ""
	}
	file, err := version.selectedFile(extension)
	if err != nil {
		return Instance{}, err
	}
	stage, err := makeCacheStage(ctx, ".pack-")
	if err != nil {
		return Instance{}, err
	}
	defer os.RemoveAll(stage)
	status("正在下载整合包清单…")
	item := file.transfer(stage)
	if err := item.ensure(ctx); err != nil {
		return Instance{}, curseDownloadError(err)
	}
	return e.ImportPack(ctx, item.target, status)
}

func (e Engine) importModrinthPack(ctx context.Context, archive, root string, data []byte, status func(string)) (Instance, error) {
	var index packIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return Instance{}, err
	}
	version := index.Dependencies["minecraft"]
	if index.FormatVersion != 1 || index.Game != "minecraft" || !segment(version) || len(index.Files) > 10000 {
		return Instance{}, fmt.Errorf("整合包清单无效或不受支持")
	}
	instance := Instance{Version: version, Name: index.Name}
	for id, build := range index.Dependencies {
		switch id {
		case "minecraft":
			continue
		case "fabric-loader", "forge", "neoforge":
			if instance.Loader != "" || !segment(build) {
				return Instance{}, fmt.Errorf("整合包要求多个加载器或加载器版本无效")
			}
			instance.Loader, instance.Build = strings.TrimSuffix(id, "-loader"), build
		default:
			return Instance{}, fmt.Errorf("该整合包要求当前版本尚未支持的 %s 加载器", id)
		}
	}
	return e.createInstance(ctx, instance, archive, func(game string) error {
		if err := installPackFiles(ctx, game, index, status); err != nil {
			return err
		}
		for _, prefix := range []string{"overrides/", "client-overrides/"} {
			if err := unpackOverrides(ctx, archive, game, root+prefix); err != nil {
				return err
			}
		}
		return nil
	}, status)
}

func installPackFiles(ctx context.Context, game string, index packIndex, status func(string)) error {
	var work []transfer
	targets := map[string]bool{}
	for _, entry := range index.Files {
		if entry.Env.Client == "unsupported" || entry.Env.Client == "optional" {
			continue
		}
		target, err := localPath(game, entry.Path)
		key := strings.ToLower(target)
		if err != nil || targets[key] || !validSHA512(entry.Hashes.SHA512) || entry.FileSize < 0 || entry.FileSize > 512<<20 {
			return fmt.Errorf("无效或重复的整合包文件 %q", entry.Path)
		}
		targets[key] = true
		var sources []string
		for _, raw := range entry.Downloads {
			addr, err := url.Parse(raw)
			if err == nil && addr.Scheme == "https" && addr.User == nil && slices.Contains(
				[]string{"cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com"}, addr.Host) {
				sources = append(sources, raw)
			}
		}
		if len(sources) == 0 {
			return fmt.Errorf("整合包文件 %q 缺少受支持的 HTTPS 下载源", entry.Path)
		}
		work = append(work, transfer{artifact{URL: sources[0], Alternates: sources[1:], Size: entry.FileSize, SHA512: entry.Hashes.SHA512}, target})
	}
	return ensureAll(ctx, work, "下载整合包资源", status)
}

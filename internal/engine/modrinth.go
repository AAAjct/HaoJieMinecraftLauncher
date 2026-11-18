package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const modrinth = "https://api.modrinth.com/v2/"

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

type ResourceLoader struct {
	Name         string
	ProjectTypes []string `json:"supported_project_types"`
}

func ResourceLoaders(ctx context.Context) ([]ResourceLoader, error) {
	var loaders []ResourceLoader
	err := fetchJSON(ctx, modrinth+"tag/loader", &loaders)
	return loaders, err
}

func ProjectIcon(ctx context.Context, source string) ([]byte, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host != "cdn.modrinth.com" || u.User != nil {
		return nil, fmt.Errorf("无效的项目图标地址")
	}
	res, err := get(ctx, source)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, 2<<20))
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
	Dependencies  []struct {
		ProjectID string `json:"project_id"`
		VersionID string `json:"version_id"`
		Type      string `json:"dependency_type"`
	}
}

type resourceFile struct {
	Filename string
	URL      string
	Primary  bool
	Size     int64
	Hashes   struct{ SHA512 string }
}

const ResourcePageSize = 12

func Search(ctx context.Context, kind, query string, filter Instance, page int) ([]Project, int, error) {
	filters := [][]string{{"project_type:" + kind}}
	if filter.Version != "" {
		filters = append(filters, []string{"versions:" + filter.Version})
	}
	if filter.Loader != "" {
		filters = append(filters, []string{"categories:" + filter.Loader})
	}
	facets, _ := json.Marshal(filters)
	q := url.Values{"query": {query}, "facets": {string(facets)}, "limit": {strconv.Itoa(ResourcePageSize)}, "offset": {strconv.Itoa(max(0, page) * ResourcePageSize)}}
	var search struct {
		Hits  []Project
		Total int `json:"total_hits"`
	}
	err := fetchJSON(ctx, modrinth+"search?"+q.Encode(), &search)
	return search.Hits, search.Total, err
}

func ResourceVersions(ctx context.Context, id, kind string, instance Instance) ([]ResourceVersion, error) {
	q := url.Values{"include_changelog": {"false"}}
	if kind == "mod" {
		for key, value := range map[string]string{"game_versions": instance.Version, "loaders": instance.Loader} {
			if value != "" {
				data, _ := json.Marshal([]string{value})
				q.Set(key, string(data))
			}
		}
	}
	var versions []ResourceVersion
	err := fetchJSON(ctx, modrinth+"project/"+url.PathEscape(id)+"/version?"+q.Encode(), &versions)
	return versions, err
}

func (v ResourceVersion) selectedFile(extension string) (resourceFile, error) {
	var candidate resourceFile
	for _, file := range v.Files {
		if !segment(file.Filename) || !strings.HasSuffix(strings.ToLower(file.Filename), extension) {
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
	if err != nil || source.Scheme != "https" || source.Host != "cdn.modrinth.com" || !validSHA512(candidate.Hashes.SHA512) {
		return resourceFile{}, fmt.Errorf("%s 没有可校验的 %s 下载", v.Name, extension)
	}
	return candidate, nil
}

func validSHA512(s string) bool {
	return len(s) == 128 && strings.Trim(s, "0123456789abcdefABCDEF") == ""
}

func (f resourceFile) transfer(dir string) transfer {
	return transfer{artifact{URL: f.URL, SHA512: f.Hashes.SHA512, Size: f.Size}, filepath.Join(dir, f.Filename)}
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
				if err := fetchJSON(ctx, modrinth+"version/"+url.PathEscape(dep.VersionID), &next); err != nil {
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
				if len(all) == 0 {
					return fmt.Errorf("找不到兼容的依赖 %s", dep.ProjectID)
				}
				next = all[0]
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
	ctx = e.resumableDownloads(ctx)
	var files []resourceFile
	var err error
	var folder string
	switch kind {
	case "mod":
		if instance.Loader == "" {
			return fmt.Errorf("请先选择带模组加载器的实例")
		}
		status("正在解析必需模组依赖…")
		files, err = resolveMods(ctx, instance, version)
		folder = "mods"
	case "shader", "resourcepack":
		var file resourceFile
		file, err = version.selectedFile(".zip")
		files = []resourceFile{file}
		folder = "shaderpacks"
		if kind == "resourcepack" {
			folder = "resourcepacks"
		}
	default:
		return fmt.Errorf("无效资源类型")
	}
	if err != nil {
		return err
	}

	game := e.GameDir(instance)
	stage, err := os.MkdirTemp(game, ".download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	targetDir := filepath.Join(game, folder)
	var work []transfer
	names := map[string]bool{}
	for _, file := range files {
		key := strings.ToLower(file.Filename)
		if names[key] {
			return fmt.Errorf("依赖包含同名文件 %s", file.Filename)
		}
		names[key] = true
		active := file.transfer(targetDir)
		disabled := active
		disabled.target += ".disabled"
		if active.ready() || (kind == "mod" && disabled.ready()) {
			continue
		}
		if regularFile(active.target) {
			return fmt.Errorf("%s 已存在，请先处理该文件", file.Filename)
		}
		work = append(work, file.transfer(stage))
	}
	if err := ensureAll(ctx, work, "下载资源", status); err != nil {
		return err
	}
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return err
	}
	var added []string
	for _, item := range work {
		target := filepath.Join(targetDir, filepath.Base(item.target))
		if err := os.Rename(item.target, target); err != nil {
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
	file, err := version.selectedFile(".mrpack")
	if err != nil {
		return Instance{}, err
	}
	if err := os.MkdirAll(e.gameRoot(), 0700); err != nil {
		return Instance{}, err
	}
	stage, err := os.MkdirTemp(e.gameRoot(), ".mrpack-")
	if err != nil {
		return Instance{}, err
	}
	defer os.RemoveAll(stage)
	status("正在下载整合包清单…")
	item := file.transfer(stage)
	if err := item.ensure(ctx); err != nil {
		return Instance{}, err
	}
	return e.ImportPack(ctx, item.target, status)
}

func (e Engine) ImportPack(ctx context.Context, path string, status func(string)) (Instance, error) {
	ctx = e.resumableDownloads(ctx)
	z, err := zip.OpenReader(path)
	if err != nil {
		return Instance{}, err
	}
	defer z.Close()
	meta, err := z.Open("modrinth.index.json")
	if err != nil {
		return Instance{}, fmt.Errorf("整合包缺少 modrinth.index.json")
	}
	var index packIndex
	err = json.NewDecoder(io.LimitReader(meta, 8<<20)).Decode(&index)
	meta.Close()
	if err != nil {
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
		case "fabric-loader", "quilt-loader", "forge":
			if instance.Loader != "" || !segment(build) {
				return Instance{}, fmt.Errorf("整合包要求多个加载器或加载器版本无效")
			}
			instance.Loader, instance.Build = strings.TrimSuffix(id, "-loader"), build
		default:
			return Instance{}, fmt.Errorf("该整合包要求当前版本尚未支持的 %s 加载器", id)
		}
	}
	return e.createInstance(ctx, instance, func(game string) error {
		return installPackFiles(ctx, game, index, z.File, status)
	}, status)
}

func installPackFiles(ctx context.Context, game string, index packIndex, archive []*zip.File, status func(string)) error {
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
			if err == nil && addr.Scheme == "https" && slices.Contains(
				[]string{"cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com"}, addr.Host) {
				sources = append(sources, raw)
			}
		}
		if len(sources) == 0 {
			return fmt.Errorf("整合包文件 %q 缺少受支持的 HTTPS 下载源", entry.Path)
		}
		work = append(work, transfer{artifact{URL: sources[0], Mirrors: sources[1:], Size: entry.FileSize, SHA512: entry.Hashes.SHA512}, target})
	}
	if err := ensureAll(ctx, work, "下载整合包资源", status); err != nil {
		return err
	}
	for _, prefix := range []string{"overrides/", "client-overrides/"} {
		for _, entry := range archive {
			if err := ctx.Err(); err != nil {
				return err
			}
			relative, hasPrefix := strings.CutPrefix(entry.Name, prefix)
			if !hasPrefix || entry.FileInfo().IsDir() {
				continue
			}
			target, err := localPath(game, relative)
			if err != nil {
				return err
			}
			if err := writeZipEntry(entry, target); err != nil {
				return err
			}
		}
	}
	return nil
}

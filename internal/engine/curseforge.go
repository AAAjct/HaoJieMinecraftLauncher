package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const cursePrefix = "curseforge:"

func curseForgeKey() string { return strings.TrimSpace(os.Getenv("CURSEFORGE_API_KEY")) }

func curseForgeHost(host string) bool {
	return host == "api.curseforge.com" || curseForgeCDN(host)
}

func curseForgeCDN(host string) bool {
	return host == "edge.forgecdn.net" || host == "mediafilez.forgecdn.net"
}

type cursePagination struct{ TotalCount int }

type curseResponse[T any] struct {
	Data       T
	Pagination cursePagination
}

func curseJSON[T any](ctx context.Context, path string) (curseResponse[T], error) {
	return curseRequest[T](ctx, http.MethodGet, path, nil)
}

func curseRequest[T any](ctx context.Context, method, path string, body []byte) (curseResponse[T], error) {
	source := func(base string) catalogSource[curseResponse[T]] {
		return func(ctx context.Context) (curseResponse[T], error) {
			var response curseResponse[T]
			var request io.Reader
			if body != nil {
				request = bytes.NewReader(body)
			}
			err := catalogRequest(ctx, method, base+path, request, &response)
			return response, err
		}
	}
	mirror := source(mcim + "/curseforge/v1/")
	if curseForgeKey() != "" {
		return preferCatalog(ctx, mirror, source("https://api.curseforge.com/v1/"))
	}
	response, err := preferCatalog(ctx, mirror)
	if err != nil {
		return response, fmt.Errorf("CurseForge 镜像请求失败（官方回退需要 CURSEFORGE_API_KEY）：%w", err)
	}
	return response, nil
}

type curseProject struct {
	ID, ClassID         int
	Name, Slug, Summary string
	DateModified        time.Time
	DownloadCount       int
	Logo                struct{ ThumbnailURL string }
	Categories          []struct{ Name string }
	LatestFilesIndexes  []struct {
		GameVersion string
		ModLoader   int
	}
}

func curseClass(kind string) int {
	switch kind {
	case "mod":
		return 6
	case "modpack":
		return 4471
	case "shader":
		return 6552
	case "resourcepack":
		return 12
	case "datapack":
		return 6945
	}
	return 0
}

var curseLoaders = []string{"", "forge", "cauldron", "liteloader", "fabric", "quilt", "neoforge"}

func SearchCurseForge(ctx context.Context, kind, query string, filter Instance, page int) ([]Project, int, error) {
	class := curseClass(kind)
	if class == 0 {
		return nil, 0, fmt.Errorf("无效资源类型")
	}
	q := url.Values{
		"gameId": {"432"}, "classId": {strconv.Itoa(class)}, "searchFilter": {query},
		"index": {strconv.Itoa(max(0, page) * ResourcePageSize)}, "pageSize": {strconv.Itoa(ResourcePageSize)},
	}
	if filter.Version != "" {
		q.Set("gameVersion", filter.Version)
	}
	if loader := slices.Index(curseLoaders, filter.Loader); loader > 0 && SupportedModLoader(filter.Loader) && filter.Version != "" && (kind == "mod" || kind == "modpack") {
		q.Set("modLoaderType", strconv.Itoa(loader))
	}
	response, err := curseJSON[[]curseProject](ctx, "mods/search?"+q.Encode())
	if err != nil {
		return nil, 0, err
	}
	projects := make([]Project, 0, len(response.Data))
	for _, row := range response.Data {
		p := Project{
			ID: cursePrefix + strconv.Itoa(row.ID), Title: row.Name, Slug: row.Slug, Description: row.Summary,
			IconURL: row.Logo.ThumbnailURL, Downloads: row.DownloadCount, DateModified: row.DateModified,
		}
		for _, category := range row.Categories {
			p.DisplayCategories = append(p.DisplayCategories, category.Name)
		}
		hasLoader := false
		for _, file := range row.LatestFilesIndexes {
			if file.GameVersion != "" && !slices.Contains(p.Versions, file.GameVersion) {
				p.Versions = append(p.Versions, file.GameVersion)
			}
			n := file.ModLoader
			hasLoader = hasLoader || n > 0
			if n > 0 && n < len(curseLoaders) {
				if loader := curseLoaders[n]; SupportedModLoader(loader) && !slices.Contains(p.Categories, loader) {
					p.Categories = append(p.Categories, loader)
				}
			}
		}
		if (kind == "mod" || kind == "modpack") && hasLoader && len(p.Categories) == 0 {
			continue
		}
		projects = append(projects, p)
	}
	return projects, min(response.Pagination.TotalCount, 10000/ResourcePageSize*ResourcePageSize), nil
}

type curseFile struct {
	ID, ModID, ReleaseType int
	DisplayName, FileName  string
	DownloadURL            string
	FileLength             int64
	IsServerPack           bool
	GameVersions           []string
	Hashes                 []struct {
		Value string
		Algo  int
	}
	Dependencies []struct{ ModID, RelationType int }
}

func (f curseFile) resourceFile() resourceFile {
	file := resourceFile{Filename: f.FileName, URL: f.DownloadURL, Size: f.FileLength, Primary: true}
	for _, hash := range f.Hashes {
		if hash.Algo == 1 && validAssetHash(strings.ToLower(hash.Value)) {
			file.SHA1 = strings.ToLower(hash.Value)
			break
		}
	}
	return file
}

func (f curseFile) version(kind string) ResourceVersion {
	v := ResourceVersion{
		ID: cursePrefix + strconv.Itoa(f.ID), ProjectID: cursePrefix + strconv.Itoa(f.ModID),
		Name: f.DisplayName, VersionNumber: f.FileName, Files: []resourceFile{f.resourceFile()},
	}
	switch f.ReleaseType {
	case 1:
		v.VersionType = "release"
	case 2:
		v.VersionType = "beta"
	case 3:
		v.VersionType = "alpha"
	}
	for _, tag := range f.GameVersions {
		lower := strings.ToLower(tag)
		if lower != "" && slices.Contains(curseLoaders, lower) || lower == "iris" || lower == "optifine" {
			v.Loaders = append(v.Loaders, lower)
		} else if tag != "" && tag[0] >= '0' && tag[0] <= '9' {
			v.GameVersions = append(v.GameVersions, tag)
		}
	}
	if kind == "datapack" {
		v.Loaders = []string{"datapack"}
	}
	for _, dep := range f.Dependencies {
		if dep.RelationType == 3 && dep.ModID > 0 {
			v.Dependencies = append(v.Dependencies, resourceDependency{ProjectID: cursePrefix + strconv.Itoa(dep.ModID), Type: "required"})
		}
	}
	return v
}

func CurseForgeVersions(ctx context.Context, id, kind string, filter Instance, page int) ([]ResourceVersion, bool, error) {
	project, err := strconv.Atoi(strings.TrimPrefix(id, cursePrefix))
	if err != nil || project <= 0 {
		return nil, false, fmt.Errorf("无效的 CurseForge 项目 ID")
	}
	const pageSize = 50
	q := url.Values{"pageSize": {strconv.Itoa(pageSize)}, "index": {strconv.Itoa(page * pageSize)}}
	if filter.Version != "" {
		q.Set("gameVersion", filter.Version)
	}
	if loader := slices.Index(curseLoaders, filter.Loader); loader > 0 && SupportedModLoader(filter.Loader) {
		q.Set("modLoaderType", strconv.Itoa(loader))
	}
	response, err := curseJSON[[]curseFile](ctx, fmt.Sprintf("mods/%d/files?%s", project, q.Encode()))
	if err != nil {
		return nil, false, err
	}
	versions := make([]ResourceVersion, 0, len(response.Data))
	for _, row := range response.Data {
		if row.IsServerPack {
			continue
		}
		v := row.version(kind)
		if kind == "modpack" && len(v.Loaders) == 0 {
			v.Loaders = []string{"minecraft"}
		}
		if row.ModID == project && row.ID > 0 && len(v.GameVersions) > 0 {
			versions = append(versions, v)
		}
	}
	return versions, (page+1)*pageSize < min(10000, response.Pagination.TotalCount), nil
}

func curseGetFiles(ctx context.Context, ids []int) ([]curseFile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(map[string][]int{"fileIds": ids})
	if err != nil {
		return nil, err
	}
	response, err := curseRequest[[]curseFile](ctx, http.MethodPost, "mods/files", payload)
	return response.Data, err
}

func curseGetFile(ctx context.Context, project, file int) (curseFile, error) {
	if project <= 0 || file <= 0 {
		return curseFile{}, fmt.Errorf("整合包包含无效的 CurseForge 文件 ID")
	}
	res, err := curseJSON[curseFile](ctx, fmt.Sprintf("mods/%d/files/%d", project, file))
	if err != nil {
		return curseFile{}, err
	}
	if res.Data.ID != file || res.Data.ModID != project {
		return curseFile{}, fmt.Errorf("CurseForge 文件 %d/%d 的元数据不匹配", project, file)
	}
	return res.Data, nil
}

func curseDownloadError(err error) error {
	var failure httpFailure
	if errors.As(err, &failure) && curseForgeCDN(failure.host) && failure.status == http.StatusUnauthorized && curseForgeKey() == "" {
		return fmt.Errorf("CurseForge 官方下载需要设置 CURSEFORGE_API_KEY：%w", err)
	}
	return err
}

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const cursePrefix = "curseforge:"

func curseForgeKey() string {
	if key := strings.TrimSpace(os.Getenv("CURSEFORGE_API_KEY")); key != "" {
		return key
	}
	return builtInCurseForgeKey
}

func curseForgeHost(host string) bool {
	return host == "api.curseforge.com" || curseForgeCDN(host)
}

func curseForgeCDN(host string) bool {
	return host == "edge.forgecdn.net" || host == "mediafilez.forgecdn.net"
}

func validCurseDownload(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.User == nil && curseForgeCDN(u.Host)
}

func curseCDNFileURL(id int, filename string) string {
	if id <= 0 || !segment(filename) {
		return ""
	}
	address := url.URL{
		Scheme: "https", Host: "mediafilez.forgecdn.net",
		Path: fmt.Sprintf("/files/%d/%d/%s", id/1000, id%1000, filename),
	}
	return address.String()
}

func curseDownloadURL(ctx context.Context, project, file int) (string, error) {
	path := fmt.Sprintf("mods/%d/files/%d/download-url", project, file)
	source := func(base string) catalogSource[string] {
		return func(ctx context.Context) (string, error) {
			var response curseResponse[string]
			if err := catalogRequest(ctx, http.MethodGet, base+path, nil, &response); err != nil {
				return "", err
			}
			if !validCurseDownload(response.Data) {
				return "", fmt.Errorf("CurseForge API 未提供可用的第三方下载地址")
			}
			return response.Data, nil
		}
	}
	sources := []catalogSource[string]{source(mcim + "/curseforge/v1/")}
	if curseForgeKey() != "" {
		sources = append(sources, source("https://api.curseforge.com/v1/"))
	}
	return preferCatalog(ctx, sources...)
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
	mirror := curseSource[T](method, mcim+"/curseforge/v1/"+path, body)
	if curseForgeKey() != "" {
		return preferCatalog(ctx, mirror, curseSource[T](method, "https://api.curseforge.com/v1/"+path, body))
	}
	response, err := preferCatalog(ctx, mirror)
	if err != nil {
		return response, fmt.Errorf("CurseForge 镜像请求失败（官方回退需要 CURSEFORGE_API_KEY）：%w", err)
	}
	return response, nil
}

func curseSource[T any](method, address string, body []byte) catalogSource[curseResponse[T]] {
	return func(ctx context.Context) (curseResponse[T], error) {
		var response curseResponse[T]
		var request io.Reader
		if body != nil {
			request = bytes.NewReader(body)
		}
		err := catalogRequest(ctx, method, address, request, &response)
		return response, err
	}
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
	Modules                []struct{ Name string }
	Hashes                 []struct {
		Value string
		Algo  int
	}
	Dependencies []struct{ ModID, RelationType int }
	publicFolder string
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
	if curseForgeKey() == "" {
		response, err := curseRequest[[]curseFile](ctx, http.MethodPost, "mods/files", payload)
		return response.Data, err
	}
	response, mirrorErr := preferCatalog(ctx, curseSource[[]curseFile](http.MethodPost, mcim+"/curseforge/v1/mods/files", payload))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found := make(map[int]bool, len(response.Data))
	for _, row := range response.Data {
		found[row.ID] = row.matches(row.ModID, row.ID)
	}
	missing := make([]int, 0, len(ids))
	for _, id := range ids {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return response.Data, nil
	}
	payload, err = json.Marshal(map[string][]int{"fileIds": missing})
	if err != nil {
		return nil, err
	}
	officialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	official, officialErr := curseSource[[]curseFile](http.MethodPost, "https://api.curseforge.com/v1/mods/files", payload)(officialCtx)
	if officialErr != nil && len(response.Data) == 0 {
		return nil, errors.Join(mirrorErr, officialErr)
	}
	return append(response.Data, official.Data...), nil
}

func curseGetFile(ctx context.Context, project, file int, gameVersion string) (curseFile, error) {
	if project <= 0 || file <= 0 {
		return curseFile{}, fmt.Errorf("整合包包含无效的 CurseForge 文件 ID")
	}
	projectFiles := fmt.Sprintf("mods/%d/files", project)
	readExact := func() (curseFile, error) {
		res, err := curseJSON[curseFile](ctx, fmt.Sprintf("%s/%d", projectFiles, file))
		if err != nil {
			return curseFile{}, err
		}
		return findCurseFile([]curseFile{res.Data}, project, file)
	}
	row, firstErr := readExact()
	if firstErr == nil {
		return row, nil
	}
	if ctx.Err() != nil {
		return curseFile{}, ctx.Err()
	}
	var publicErr error
	if curseTransient(firstErr) {
		if row, publicErr = cursePublicFile(ctx, project, file); publicErr == nil {
			return row, nil
		}
	}
	rows, batchErr := curseGetFiles(ctx, []int{file})
	if batchErr == nil {
		if row, batchErr = findCurseFile(rows, project, file); batchErr == nil {
			return row, nil
		}
	}
	if ctx.Err() != nil {
		return curseFile{}, ctx.Err()
	}
	query := url.Values{"pageSize": {"50"}, "gameVersion": {gameVersion}}
	res, listErr := curseJSON[[]curseFile](ctx, projectFiles+"?"+query.Encode())
	if listErr == nil {
		if row, listErr = findCurseFile(res.Data, project, file); listErr == nil {
			return row, nil
		}
	}
	var retryErr error
	if curseTransient(firstErr) {
		timer := time.NewTimer(350 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return curseFile{}, ctx.Err()
		case <-timer.C:
		}
		if row, retryErr = readExact(); retryErr == nil {
			return row, nil
		}
	}
	if ctx.Err() != nil {
		return curseFile{}, ctx.Err()
	}
	if publicErr == nil {
		if row, publicErr = cursePublicFile(ctx, project, file); publicErr == nil {
			return row, nil
		}
	}
	return curseFile{}, fmt.Errorf("无法取得 CurseForge 文件信息：%w", errors.Join(firstErr, batchErr, listErr, retryErr, publicErr))
}

var curseWidgetSlots = make(chan struct{}, 2)

func cursePublicFile(ctx context.Context, project, file int) (curseFile, error) {
	select {
	case curseWidgetSlots <- struct{}{}:
		defer func() { <-curseWidgetSlots }()
	case <-ctx.Done():
		return curseFile{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var data struct {
		ID         int
		Game, Type string
		Files      []struct {
			ID       int
			Name     string
			FileSize int64
		}
	}
	if err := catalogJSON(ctx, "https://api.cfwidget.com/"+strconv.Itoa(project), &data); err != nil {
		return curseFile{}, err
	}
	var folder, extension string
	switch strings.ToLower(data.Type) {
	case "mods":
		folder, extension = "mods", ".jar"
	case "texture packs", "resource packs":
		folder, extension = "resourcepacks", ".zip"
	case "shaders":
		folder, extension = "shaderpacks", ".zip"
	}
	if data.ID == project && data.Game == "minecraft" && folder != "" {
		for _, entry := range data.Files {
			if entry.ID == file && entry.FileSize > 0 && entry.FileSize <= packEntryLimit &&
				segment(entry.Name) && strings.EqualFold(filepath.Ext(entry.Name), extension) {
				return curseFile{ID: file, ModID: project, FileName: entry.Name, FileLength: entry.FileSize,
					DownloadURL: curseCDNFileURL(file, entry.Name), publicFolder: folder}, nil
			}
		}
	}
	return curseFile{}, fmt.Errorf("CFWidget 缺少 CurseForge 文件 %d/%d 的公开记录", project, file)
}

func curseTransient(err error) bool {
	var failure httpFailure
	if errors.As(err, &failure) {
		return failure.status == http.StatusTooManyRequests || failure.status >= 500
	}
	var networkError net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError)
}

func (f curseFile) matches(project, file int) bool {
	return f.ID == file && f.ModID == project && segment(f.FileName) && f.FileLength >= 0 &&
		validAssetHash(f.resourceFile().SHA1)
}

func findCurseFile(rows []curseFile, project, file int) (curseFile, error) {
	for _, row := range rows {
		if row.matches(project, file) {
			return row, nil
		}
	}
	return curseFile{}, fmt.Errorf("响应缺少文件 %d/%d 的文件名或 SHA-1", project, file)
}

func (e Engine) curseFileCachePath(project, file int) string {
	return filepath.Join(e.cacheDir(), "curseforge", fmt.Sprintf("%d-%d.json", project, file))
}

func (e Engine) cacheCurseFile(row curseFile) {
	// Signed URLs can expire independently of the file's immutable SHA-1.
	if address, err := url.Parse(row.DownloadURL); err == nil && address.RawQuery != "" {
		return
	}
	target := e.curseFileCachePath(row.ModID, row.ID)
	if os.MkdirAll(filepath.Dir(target), 0700) == nil {
		_ = writeJSON(target, row)
	}
}

func curseDownloadError(err error) error {
	var failure httpFailure
	if errors.As(err, &failure) && curseForgeCDN(failure.host) && failure.status == http.StatusUnauthorized && curseForgeKey() == "" {
		return fmt.Errorf("CurseForge 官方下载需要设置 CURSEFORGE_API_KEY：%w", err)
	}
	return err
}

package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nwaples/rardecode/v2"
)

const packEntryLimit = 512 << 20

type packEntry struct {
	name string
	mode fs.FileMode
	size int64
	open func() (io.ReadCloser, error)
}

func walkPack(ctx context.Context, file string, visit func(packEntry) error) error {
	if strings.EqualFold(filepath.Ext(file), ".rar") {
		archive, err := rardecode.OpenReader(file, rardecode.MaxDictionarySize(64<<20))
		if err != nil {
			return err
		}
		defer archive.Close()
		for ctx.Err() == nil {
			header, err := archive.Next()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			entry := packEntry{header.Name, header.Mode(), header.UnPackedSize, func() (io.ReadCloser, error) {
				return io.NopCloser(archive), nil
			}}
			if err := visit(entry); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	archive, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(packEntry{entry.Name, entry.Mode(), int64(entry.UncompressedSize64), entry.Open}); err != nil {
			return err
		}
	}
	return nil
}

func (entry packEntry) openLimited(limit int64) (io.ReadCloser, error) {
	if !entry.mode.IsRegular() || entry.size < 0 || entry.size > limit {
		return nil, fmt.Errorf("不支持的压缩条目 %s", entry.name)
	}
	return entry.open()
}

var errPackManifestFound = errors.New("找到整合包清单")

func (e Engine) ImportPack(ctx context.Context, file string, status func(string)) (Instance, error) {
	ctx = e.resumableDownloads(ctx)
	status("正在读取整合包清单…")
	var manifest, root string
	var data []byte
	err := walkPack(ctx, file, func(entry packEntry) error {
		dir, name := path.Split(entry.name)
		if name != "modrinth.index.json" && name != "manifest.json" {
			return nil
		}
		if dir != "" && (!segment(strings.TrimSuffix(dir, "/")) || manifest != "") {
			return nil
		}
		const limit = 8 << 20
		reader, err := entry.openLimited(limit)
		if err != nil {
			return err
		}
		defer reader.Close()
		body, err := io.ReadAll(io.LimitReader(cancellableReader{ctx, reader}, limit+1))
		if err != nil {
			return err
		}
		if len(body) > limit {
			return fmt.Errorf("整合包清单过大")
		}
		manifest, root, data = name, dir, body
		if dir == "" {
			return errPackManifestFound
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPackManifestFound) {
		return Instance{}, err
	}
	switch manifest {
	case "modrinth.index.json":
		return e.importModrinthPack(ctx, file, root, data, status)
	case "manifest.json":
		return e.importCursePack(ctx, file, root, data, status)
	default:
		return Instance{}, fmt.Errorf("整合包缺少 Modrinth 或 CurseForge 清单")
	}
}

func unpackOverrides(ctx context.Context, file, game, prefix string) error {
	return walkPack(ctx, file, func(entry packEntry) error {
		relative, ok := strings.CutPrefix(entry.name, prefix)
		if !ok || entry.mode.IsDir() {
			return nil
		}
		target, err := localPath(game, relative)
		if err != nil {
			return err
		}
		// Windows can alias names ending in periods or spaces.
		for part := range strings.SplitSeq(relative, "/") {
			if strings.TrimRight(part, " .") != part {
				return fmt.Errorf("无效的压缩条目路径 %q", entry.name)
			}
		}
		reader, err := entry.openLimited(packEntryLimit)
		if err != nil {
			return err
		}
		defer reader.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		output, err := os.Create(target)
		if err != nil {
			return err
		}
		count, copyErr := io.Copy(output, io.LimitReader(cancellableReader{ctx, reader}, packEntryLimit+1))
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if count > packEntryLimit {
			return fmt.Errorf("压缩条目 %s 超出大小限制", entry.name)
		}
		return closeErr
	})
}

type curseManifest struct {
	ManifestType    string
	ManifestVersion int
	Name, Overrides string
	Minecraft       struct {
		Version    string
		ModLoaders []struct {
			ID      string
			Primary bool
		}
	}
	Files []struct {
		ProjectID, FileID int
		Required          *bool
	}
}

func (e Engine) importCursePack(ctx context.Context, archive, root string, data []byte, status func(string)) (Instance, error) {
	var meta curseManifest
	if err := json.Unmarshal(data, &meta); err != nil {
		return Instance{}, err
	}
	if meta.ManifestType != "minecraftModpack" || meta.ManifestVersion != 1 ||
		!segment(meta.Minecraft.Version) || len(meta.Files) > 10000 {
		return Instance{}, fmt.Errorf("CurseForge 整合包清单无效或不受支持")
	}
	instance := Instance{Version: meta.Minecraft.Version, Name: meta.Name}
	loaders := meta.Minecraft.ModLoaders
	if len(loaders) > 0 {
		selected := loaders[0]
		for _, loader := range loaders {
			if loader.Primary {
				selected = loader
				break
			}
		}
		id, build, ok := strings.Cut(selected.ID, "-")
		switch id {
		case "forge", "fabric", "neoforge":
			if !ok || !segment(build) {
				return Instance{}, fmt.Errorf("CurseForge 整合包的加载器版本无效")
			}
			if id == "neoforge" && instance.Version == "1.20.1" {
				build = strings.TrimPrefix(build, instance.Version+"-")
			}
			instance.Loader, instance.Build = id, build
		default:
			return Instance{}, fmt.Errorf("当前版本尚未支持 %s 加载器", id)
		}
	}
	if meta.Overrides == "" {
		meta.Overrides = "overrides"
	}
	if filepath.IsAbs(meta.Overrides) || !fs.ValidPath(meta.Overrides) || meta.Overrides == "." || strings.Contains(meta.Overrides, "\\") {
		return Instance{}, fmt.Errorf("无效的 CurseForge 覆盖目录")
	}
	return e.createInstance(ctx, instance, func(game string) error {
		status("正在读取 CurseForge 整合包文件…")
		required := meta.Files[:0]
		for _, entry := range meta.Files {
			if entry.Required != nil && !*entry.Required {
				continue
			}
			if entry.ProjectID <= 0 || entry.FileID <= 0 {
				return fmt.Errorf("整合包包含无效的 CurseForge 文件 ID")
			}
			required = append(required, entry)
		}
		const batchSize = 50
		batches := slices.Collect(slices.Chunk(required, batchSize))
		work := make([]transfer, len(required))
		err := fetchPackEntries(ctx, len(batches), func(ctx context.Context, i int) error {
			batch := batches[i]
			ids := make([]int, 0, len(batch))
			for _, entry := range batch {
				ids = append(ids, entry.FileID)
			}
			files, err := curseGetFiles(ctx, ids)
			if err != nil {
				return err
			}
			byID := make(map[int]curseFile, len(files))
			for _, file := range files {
				byID[file.ID] = file
			}
			for n, entry := range batch {
				row, ok := byID[entry.FileID]
				if !ok {
					row, err = curseGetFile(ctx, entry.ProjectID, entry.FileID)
					if err != nil {
						return fmt.Errorf("CurseForge 文件 %d/%d：%w", entry.ProjectID, entry.FileID, err)
					}
				}
				if row.ModID != entry.ProjectID {
					return fmt.Errorf("CurseForge 文件 %d/%d 的元数据不匹配", entry.ProjectID, entry.FileID)
				}
				item, err := cursePackTransfer(ctx, game, row)
				if err != nil {
					return err
				}
				work[i*batchSize+n] = item
			}
			return nil
		})
		if err != nil {
			return err
		}
		seen := make(map[string]bool, len(work))
		for _, item := range work {
			key := strings.ToLower(item.target)
			if seen[key] {
				return fmt.Errorf("CurseForge 整合包包含重复文件 %s", filepath.Base(item.target))
			}
			seen[key] = true
		}
		if err := ensureAll(ctx, work, "下载整合包资源", status); err != nil {
			return curseDownloadError(err)
		}
		return unpackOverrides(ctx, archive, game, root+meta.Overrides+"/")
	}, status)
}

func cursePackTransfer(ctx context.Context, game string, row curseFile) (transfer, error) {
	file := row.resourceFile()
	if file.URL == "" && row.ModID > 0 && row.ID > 0 && segment(file.Filename) && validAssetHash(file.SHA1) {
		file.URL = fmt.Sprintf("https://www.curseforge.com/api/v1/mods/%d/files/%d/download", row.ModID, row.ID)
	} else {
		var err error
		file, err = row.version("mod").selectedFile("")
		if err != nil {
			return transfer{}, err
		}
	}
	folder := "mods"
	if !strings.EqualFold(filepath.Ext(file.Filename), ".jar") {
		project, err := curseJSON[curseProject](ctx, fmt.Sprintf("mods/%d", row.ModID))
		if err != nil {
			return transfer{}, err
		}
		switch project.Data.ClassID {
		case 12:
			folder = "resourcepacks"
		case 6552:
			folder = "shaderpacks"
		default:
			return transfer{}, fmt.Errorf("不支持自动安装整合包文件 %s", file.Filename)
		}
	}
	return file.transfer(filepath.Join(game, folder)), nil
}

func fetchPackEntries(ctx context.Context, count int, fetch func(context.Context, int) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(count, 8) {
		wg.Go(func() {
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= count {
					return
				}
				if err := fetch(ctx, i); err != nil {
					cancel(err)
					return
				}
			}
		})
	}
	wg.Wait()
	return context.Cause(ctx)
}

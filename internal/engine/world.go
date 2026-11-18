package engine

import (
	"archive/zip"
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (e Engine) Worlds(instance Instance) ([]string, error) {
	root, err := os.OpenRoot(e.GameDir(instance))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), "saves")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var worlds []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := root.Stat(filepath.Join("saves", entry.Name(), "level.dat"))
		if err == nil && info.Mode().IsRegular() {
			worlds = append(worlds, entry.Name())
		}
	}
	return worlds, nil
}

func (e Engine) DownloadWorld(ctx context.Context, instance Instance, source string, status func(string)) (string, error) {
	address, err := url.Parse(source)
	if err != nil || address.Scheme != "https" || address.Hostname() == "" || address.User != nil {
		return "", fmt.Errorf("请输入 Java 版世界压缩包的 HTTPS 下载链接")
	}
	game := e.GameDir(instance)
	root, err := os.OpenRoot(game)
	if err != nil {
		return "", err
	}
	defer root.Close()
	stage, err := os.MkdirTemp(game, ".world-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	archive := transfer{artifact: artifact{URL: source}, target: filepath.Join(stage, "world.zip")}
	if err := ensureAll(ctx, []transfer{archive}, "下载世界", status); err != nil {
		return "", err
	}
	status("正在解压世界存档…")
	name, err := extractWorld(ctx, archive.target, filepath.Join(stage, "world"))
	if err != nil {
		return "", err
	}
	if name == "." {
		file := path.Base(address.Path)
		name = strings.TrimSuffix(file, path.Ext(file))
	}
	if !segment(name) || strings.TrimRight(name, " .") != name {
		name = "下载的世界"
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := root.MkdirAll("saves", 0700); err != nil {
		return "", err
	}
	target := filepath.Join("saves", name)
	if _, err := root.Lstat(target); err == nil {
		return "", fmt.Errorf("存档 %s 已存在，请重命名已有存档后再安装", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := root.Rename(filepath.Join(filepath.Base(stage), "world"), target); err != nil {
		return "", err
	}
	return name, nil
}

func extractWorld(ctx context.Context, archive, destination string) (string, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return "", fmt.Errorf("下载内容不是可读取的 ZIP 世界压缩包：%w", err)
	}
	defer z.Close()
	world := ""
	for _, entry := range z.File {
		if entry.Mode().IsRegular() && path.Base(entry.Name) == "level.dat" && !strings.HasPrefix(entry.Name, "__MACOSX/") {
			if world != "" {
				return "", fmt.Errorf("ZIP 内包含多个世界，请使用只包含一个世界的压缩包")
			}
			world = path.Dir(entry.Name)
		}
	}
	if world == "" {
		return "", fmt.Errorf("ZIP 内找不到 Java 版世界所需的 level.dat")
	}
	var total uint64
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if entry.FileInfo().IsDir() || strings.HasPrefix(entry.Name, "__MACOSX/") {
			continue
		}
		// Validate the original path before removing the archive's wrapping folder.
		if _, err := filepath.Localize(entry.Name); err != nil {
			return "", err
		}
		relative := entry.Name
		if world != "." {
			var inside bool
			relative, inside = strings.CutPrefix(relative, world+"/")
			if !inside {
				continue
			}
		}
		target, err := localPath(destination, relative)
		if err != nil {
			return "", err
		}
		if entry.UncompressedSize64 > 4<<30-total {
			return "", fmt.Errorf("世界解压后超过 4 GiB")
		}
		total += entry.UncompressedSize64
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			return "", fmt.Errorf("ZIP 内包含重复或冲突的路径 %q", entry.Name)
		}
		if err := writeZipEntry(entry, target); err != nil {
			return "", err
		}
	}
	return path.Base(world), nil
}

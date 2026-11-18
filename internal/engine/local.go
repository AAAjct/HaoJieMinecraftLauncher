package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func installFabricAPI(ctx context.Context, instance Instance, game string, status func(string)) error {
	dir := filepath.Join(game, "mods")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(entry.Name()), ".jar") {
			var meta struct{ ID string }
			if readModJSON(filepath.Join(dir, entry.Name()), &meta) == nil && meta.ID == "fabric-api" {
				return nil
			}
		}
	}
	status("正在匹配 Fabric API…")
	versions, err := ResourceVersions(ctx, "fabric-api", "mod", instance)
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return fmt.Errorf("Minecraft %s 暂无兼容的 Fabric API", instance.Version)
	}
	index := slices.IndexFunc(versions, func(v ResourceVersion) bool { return v.VersionType == "release" })
	file, err := versions[max(0, index)].selectedFile(".jar")
	if err != nil {
		return err
	}
	status("正在下载 Fabric API · " + file.Filename)
	return file.transfer(dir).ensure(ctx)
}

func readModJSON(path string, meta any) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	body, err := z.Open("fabric.mod.json")
	if err != nil {
		return err
	}
	defer body.Close()
	return json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(meta)
}

func (e Engine) InstallLocalMods(ctx context.Context, instance Instance, paths []string) error {
	if instance.ID == "" || len(paths) == 0 {
		return fmt.Errorf("请选择要安装模组的实例")
	}
	dir := filepath.Join(e.GameDir(instance), "mods")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dir, ".hjml-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	names := map[string]bool{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := filepath.Base(path)
		key := strings.ToLower(name)
		if !strings.HasSuffix(key, ".jar") || !segment(name) || !regularFile(path) || names[key] {
			return fmt.Errorf("无效或重复的模组 %q", name)
		}
		names[key] = true
		for _, suffix := range []string{"", ".disabled"} {
			if _, err := os.Lstat(filepath.Join(dir, name+suffix)); !os.IsNotExist(err) {
				return fmt.Errorf("目标实例中已有 %s，或无法访问同名文件", name)
			}
		}
		if err := stageLocalJar(ctx, path, filepath.Join(stage, name)); err != nil {
			return err
		}
	}
	resourcePublishMu.Lock()
	defer resourcePublishMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, path := range paths {
		name := filepath.Base(path)
		for _, suffix := range []string{"", ".disabled"} {
			if _, err := os.Lstat(filepath.Join(dir, name+suffix)); !os.IsNotExist(err) {
				return fmt.Errorf("目标实例中已有 %s，或无法访问同名文件", name)
			}
		}
	}
	var published []string
	for _, path := range paths {
		name := filepath.Base(path)
		target := filepath.Join(dir, name)
		if err := os.Link(filepath.Join(stage, name), target); err != nil {
			for _, file := range published {
				_ = os.Remove(file)
			}
			return fmt.Errorf("无法安装 %s：%w", name, err)
		}
		published = append(published, target)
	}
	return nil
}

type cancellableReader struct {
	context.Context
	io.Reader
}

func (r cancellableReader) Read(p []byte) (int, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func stageLocalJar(ctx context.Context, source, target string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	z, err := zip.NewReader(file, info.Size())
	if err != nil || len(z.File) == 0 {
		return fmt.Errorf("%s 不是可读取的 JAR 文件", filepath.Base(source))
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, cancellableReader{ctx, file})
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

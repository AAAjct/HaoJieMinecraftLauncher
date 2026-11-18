package engine

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const forgeMaven = "https://maven.minecraftforge.net/net/minecraftforge/forge/"

var forgeBuild = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)

func recommendedForge(ctx context.Context, version string) (string, error) {
	var promotions struct{ Promos map[string]string }
	addr := "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json"
	if err := fetchJSON(ctx, addr, &promotions); err != nil {
		return "", fmt.Errorf("读取 Forge 版本列表：%w", err)
	}
	for _, tag := range []string{"-recommended", "-latest"} {
		if build := promotions.Promos[version+tag]; forgeBuild.MatchString(build) {
			return build, nil
		}
	}
	return "", fmt.Errorf("Forge 暂无 Minecraft %s 的推荐或最新构建", version)
}

func (e Engine) installForge(ctx context.Context, i Instance, base gameProfile, status func(string)) (gameProfile, error) {
	var overlay gameProfile
	if !forgeBuild.MatchString(i.Build) {
		return overlay, fmt.Errorf("Forge 构建版本无效")
	}
	build := i.Version + "-" + i.Build
	addr := forgeMaven + build + "/forge-" + build + "-installer.jar"
	res, err := get(ctx, addr+".sha1")
	if err != nil {
		return overlay, err
	}
	sha, err := io.ReadAll(io.LimitReader(res.Body, 512))
	res.Body.Close()
	checksum := strings.ToLower(strings.TrimSpace(string(sha)))
	if err != nil || !validAssetHash(checksum) {
		return overlay, fmt.Errorf("Forge 安装器缺少有效的 SHA-1")
	}
	cacheRoot := filepath.Join(e.gameRoot(), "forge")
	installer := filepath.Join(cacheRoot, build+"-installer.jar")
	status("下载 Forge 安装器 · " + i.Build)
	if err := (transfer{artifact{URL: addr, SHA1: checksum}, installer}).ensure(ctx); err != nil {
		return overlay, err
	}
	z, err := zip.OpenReader(installer)
	if err != nil {
		return overlay, err
	}
	body, err := z.Open("version.json")
	if err != nil {
		z.Close()
		return overlay, fmt.Errorf("Forge %s 的旧版安装格式暂未支持", i.Build)
	}
	err = json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&overlay)
	body.Close()
	z.Close()
	if err != nil || !segment(overlay.ID) || overlay.InheritsFrom != i.Version {
		return overlay, fmt.Errorf("Forge 安装器中的版本文件无效")
	}
	java, err := e.EnsureJava(ctx, base.JavaVersion.MajorVersion, status)
	if err != nil {
		return overlay, err
	}
	stage, err := os.MkdirTemp(cacheRoot, ".forge-")
	if err != nil {
		return overlay, err
	}
	defer os.RemoveAll(stage)
	if err := os.WriteFile(filepath.Join(stage, "launcher_profiles.json"), []byte(`{"profiles":{}}`), 0600); err != nil {
		return overlay, err
	}
	jar := e.baseVersionFile(i.Version, ".jar")
	if err := (transfer{base.Downloads.Client, jar}).ensure(ctx); err != nil {
		return overlay, err
	}
	vanilla := filepath.Join(stage, "versions", i.Version)
	if err := os.MkdirAll(vanilla, 0700); err != nil {
		return overlay, err
	}
	if err := os.Link(jar, filepath.Join(vanilla, i.Version+".jar")); err != nil {
		return overlay, err
	}
	status("正在执行 Forge 安装处理器…")
	if err := runForge(ctx, java, installer, stage, filepath.Join(cacheRoot, build+"-install-*.log"), status); err != nil {
		return overlay, err
	}
	if err := readJSON(filepath.Join(stage, "versions", overlay.ID, overlay.ID+".json"), &overlay); err != nil {
		return overlay, err
	}
	libRoot := filepath.Join(stage, "libraries")
	err = filepath.WalkDir(libRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("Forge 生成了无效的库文件")
		}
		relative, err := filepath.Rel(libRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(e.gameRoot(), "libraries", relative)
		if regularFile(target) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.Link(path, target); err != nil && !regularFile(target) {
			return err
		}
		return nil
	})
	return overlay, err
}

func runForge(ctx context.Context, java, installer, stage, logPath string, status func(string)) error {
	log, err := os.CreateTemp(filepath.Dir(logPath), filepath.Base(logPath))
	if err != nil {
		return err
	}
	defer log.Close()
	logPath = log.Name()
	cmd := exec.CommandContext(ctx, filepath.Join(java, "bin", "java.exe"), "-Djava.awt.headless=true", "-jar", installer, "--installClient", stage)
	cmd.Dir = stage
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsNoConsole}
	reader, writer := io.Pipe()
	defer reader.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		writer.Close()
		return err
	}
	result := make(chan error, 1)
	go func() {
		result <- cmd.Wait()
		writer.Close()
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	success := false
	var last time.Time
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(log, line)
		if strings.Contains(line, "Successfully installed client") {
			success = true
		}
		if time.Since(last) > 250*time.Millisecond && len(line) < 150 &&
			(strings.HasPrefix(line, "Processor:") || strings.HasPrefix(line, "Considering ")) {
			status("Forge · " + line)
			last = time.Now()
		}
	}
	if scanner.Err() != nil {
		io.Copy(io.Discard, reader)
	}
	if err := <-result; err != nil || !success {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("Forge 安装失败，请查看 %s", logPath)
	}
	return nil
}

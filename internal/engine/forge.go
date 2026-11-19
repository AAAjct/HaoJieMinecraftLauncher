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

func (e Engine) installForgeFamily(ctx context.Context, i Instance, base gameProfile, status func(string)) (gameProfile, error) {
	var overlay gameProfile
	name, artifactName, repository := "Forge", "forge", forgeMaven
	group := "net/minecraftforge"
	build := i.Version + "-" + i.Build
	if i.Loader == "neoforge" {
		name = "NeoForge"
		group = "net/neoforged"
		artifactName = neoForgeArtifact(i.Version)
		repository = neoForgeMaven + artifactName + "/"
		if !neoForgeMatches(i.Version, i.Build) {
			return overlay, fmt.Errorf("NeoForge %s 与 Minecraft %s 不兼容", i.Build, i.Version)
		}
		if i.Version != "1.20.1" {
			build = i.Build
		}
	} else if !forgeBuild.MatchString(i.Build) {
		return overlay, fmt.Errorf("Forge 构建版本无效")
	}
	prefix := artifactName + "-" + build
	addr := repository + build + "/" + prefix + "-installer.jar"
	shared := filepath.Join(e.gameRoot(), "libraries")
	cacheBuild := build
	if i.Loader == "neoforge" {
		cacheBuild = i.Build
	}
	cacheRoot := filepath.Join(shared, group, i.Loader, cacheBuild)
	installer := filepath.Join(cacheRoot, prefix+"-installer.jar")
	status("下载 " + name + " 安装器 · " + i.Build)
	if err := downloadForgeInstaller(ctx, addr, installer); err != nil {
		return overlay, fmt.Errorf("下载 %s 安装器：%w", name, err)
	}
	z, err := zip.OpenReader(installer)
	if err != nil {
		return overlay, err
	}
	defer z.Close()
	if err := installerJSON(&z.Reader, "version.json", &overlay); err != nil {
		return overlay, fmt.Errorf("读取 %s 安装器中的版本文件：%w", name, err)
	}
	if !segment(overlay.ID) || overlay.InheritsFrom != i.Version {
		return overlay, fmt.Errorf("%s 安装器中的版本文件无效", name)
	}
	var installProfile struct{ Libraries []library }
	if err := installerJSON(&z.Reader, "install_profile.json", &installProfile); err != nil {
		return overlay, fmt.Errorf("读取 %s 安装清单：%w", name, err)
	}
	java, err := e.EnsureJava(ctx, base.JavaVersion.MajorVersion, status)
	if err != nil {
		return overlay, err
	}
	stage, err := os.MkdirTemp(cacheRoot, ".installer-")
	if err != nil {
		return overlay, err
	}
	defer os.RemoveAll(stage)
	if err := os.WriteFile(filepath.Join(stage, "launcher_profiles.json"), []byte(`{"profiles":{}}`), 0600); err != nil {
		return overlay, err
	}
	jar := e.baseVersionFile(i.Version, ".jar")
	if err := (transfer{minecraftClient(base), jar}).ensure(ctx); err != nil {
		return overlay, err
	}
	vanilla := filepath.Join(stage, "versions", i.Version)
	if err := os.MkdirAll(vanilla, 0700); err != nil {
		return overlay, err
	}
	if err := os.Link(jar, filepath.Join(vanilla, i.Version+".jar")); err != nil {
		return overlay, err
	}
	description, err := os.ReadFile(e.baseVersionFile(i.Version, ".json"))
	if err != nil {
		return overlay, err
	}
	if err := os.WriteFile(filepath.Join(vanilla, i.Version+".json"), description, 0600); err != nil {
		return overlay, err
	}
	libRoot := filepath.Join(stage, "libraries")
	libraries := append(append(installProfile.Libraries, overlay.Libraries...), base.Libraries...)
	if err := e.prepareInstallerLibraries(ctx, &z.Reader, libRoot, libraries, status); err != nil {
		return overlay, err
	}
	status("正在执行 " + name + " 安装处理器…")
	if err := runForgeInstaller(ctx, name, java, installer, stage, filepath.Join(cacheRoot, prefix+"-install-*.log"), status); err != nil {
		return overlay, err
	}
	if err := readJSON(filepath.Join(stage, "versions", overlay.ID, overlay.ID+".json"), &overlay); err != nil {
		return overlay, err
	}
	err = filepath.WalkDir(libRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s 生成了无效的库文件", name)
		}
		relative, err := filepath.Rel(libRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(shared, relative)
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

func downloadForgeInstaller(ctx context.Context, addr, target string) error {
	mirror := minecraftMirror(addr)
	var sources []catalogSource[string]
	for _, address := range []string{mirror, neoForgeCDN(addr), addr} {
		if address == "" {
			continue
		}
		sources = append(sources, func(ctx context.Context) (string, error) {
			res, err := get(ctx, address+".sha1")
			if err != nil {
				return "", err
			}
			defer res.Body.Close()
			sha, err := io.ReadAll(io.LimitReader(res.Body, 512))
			if err != nil {
				return "", err
			}
			checksum := strings.ToLower(strings.TrimSpace(string(sha)))
			if !validAssetHash(checksum) {
				return "", fmt.Errorf("%s 返回的安装器 SHA-1 无效", address)
			}
			return checksum, nil
		})
	}
	checksum, err := preferCatalog(ctx, sources...)
	if err != nil {
		return err
	}
	file := withMinecraftMirror(artifact{URL: addr, SHA1: checksum}, mirror)
	return (transfer{file, target}).ensure(ctx)
}

func installerJSON(archive *zip.Reader, name string, out any) error {
	file, err := archive.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(io.LimitReader(file, 8<<20)).Decode(out)
}

func (e Engine) prepareInstallerLibraries(ctx context.Context, archive *zip.Reader, target string, libraries []library, status func(string)) error {
	shared := filepath.Join(e.gameRoot(), "libraries")
	var work []transfer
	seen := make(map[string]bool)
	for _, lib := range libraries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !permitted(lib.Rules, nil) {
			continue
		}
		artifacts := []artifact{lib.Downloads.Artifact}
		if classifier := lib.Natives["windows"]; classifier != "" {
			artifacts = append(artifacts, lib.Downloads.Classifiers[strings.ReplaceAll(classifier, "${arch}", "64")])
		}
		for _, file := range artifacts {
			if file.Path == "" || file.URL == "" {
				continue
			}
			key := strings.ToLower(file.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			path, err := localPath(target, file.Path)
			if err != nil {
				return err
			}
			if embedded, err := archive.Open("maven/" + file.Path); err == nil {
				embedded.Close()
				continue
			}
			cache := transfer{file, filepath.Join(shared, filepath.FromSlash(file.Path))}
			if digest, _ := cache.digest(); digest != nil && cache.ready() {
				verified, _ := installedResource(cache, false)
				if verified {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						return err
					}
					if err := os.Link(cache.target, path); err == nil {
						continue
					}
				}
			}
			work = append(work, transfer{withMinecraftMirror(file, minecraftMirror(file.URL)), path})
		}
	}
	return ensureAll(ctx, work, "下载加载器运行库", status)
}

func runForgeInstaller(ctx context.Context, name, java, installer, stage, logPath string, status func(string)) error {
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
			status(name + " · " + line)
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
		return fmt.Errorf("%s 安装失败，请查看 %s", name, logPath)
	}
	return nil
}

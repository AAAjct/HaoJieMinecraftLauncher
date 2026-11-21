package engine

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const (
	azulAPI = "https://api.azul.com/metadata/v1/zulu/packages/"
)

var javaMajors = []int{8, 17, 21, 25}

var packageID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func (e Engine) javaHome(major int) string {
	return filepath.Join(e.javaRoot(), fmt.Sprintf("zulu %d", major))
}

func (e Engine) temurinHome(major int) string {
	return filepath.Join(e.javaRoot(), fmt.Sprintf("temurin %d", major))
}

func (e Engine) javaRoot() string {
	if e.JavaRoot != "" {
		return e.JavaRoot
	}
	return filepath.Join(e.DataDir, "jdk")
}

func isJDK(home string, major int) bool {
	for _, executable := range []string{"java.exe", "javac.exe"} {
		info, err := os.Stat(filepath.Join(home, "bin", executable))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false
		}
	}
	release, err := os.ReadFile(filepath.Join(home, "release"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(release), "\n") {
		if version, ok := strings.CutPrefix(line, "JAVA_VERSION="); ok {
			version = strings.Trim(strings.TrimSpace(version), `"`)
			version = strings.TrimPrefix(version, "1.")
			majorText, _, _ := strings.Cut(version, ".")
			return majorText == strconv.Itoa(major)
		}
	}
	return false
}

func (e Engine) InstalledJava() []int {
	var versions []int
	for _, major := range javaMajors {
		if isJDK(e.temurinHome(major), major) || isJDK(e.javaHome(major), major) {
			versions = append(versions, major)
		}
	}
	return versions
}

func launcherJavaMajor(required int) (int, error) {
	for _, major := range javaMajors {
		if required <= major && required >= 0 {
			return major, nil
		}
	}
	return 0, fmt.Errorf("无法满足 Java %d，启动器提供 JDK 8、17、21 和 25", required)
}

func (e Engine) EnsureJava(ctx context.Context, major int, status func(string)) (string, error) {
	major, err := launcherJavaMajor(major)
	if err != nil {
		return "", err
	}
	ctx = e.resumableDownloads(ctx)
	target := e.javaHome(major)
	parent := filepath.Dir(target)
	if err := normalizeDirectoryCase(ctx, parent); err != nil {
		return "", err
	}
	reuse := func(target string) (string, error) {
		if err := normalizeDirectoryCase(ctx, target); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return target, nil
	}
	installed := func() string {
		for _, home := range []string{e.temurinHome(major), target} {
			if isJDK(home, major) {
				return home
			}
		}
		return ""
	}
	if home := installed(); home != "" {
		return reuse(home)
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(parent, fmt.Sprintf(".zulu-%d.lock", major)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	var stage string
	cleanup := true
	defer func() {
		// A caller can use a published JDK while the installer removes its archive.
		lock.Close()
		if cleanup && stage != "" {
			os.RemoveAll(stage)
		}
	}()
	label := fmt.Sprintf("JDK %d", major)
	waiting := false
	locked, err := waitFileLock(ctx, lock, func() bool {
		if installed() != "" {
			return true
		}
		if !waiting {
			waiting = true
			status("正在等待其他任务安装 " + label + "…")
		}
		return false
	})
	if err != nil {
		return "", err
	}
	if home := installed(); !locked || home != "" {
		if waiting {
			status("正在复用已安装的 " + label + "…")
		}
		return reuse(home)
	}
	status("正在查询 " + label + "…")
	stage, err = makeCacheStage(ctx, ".jdk-")
	if err != nil {
		return "", err
	}
	name, item, temurinErr := temurinDownload(ctx, major)
	archive := filepath.Join(stage, name)
	if temurinErr == nil {
		temurinErr = ensureAll(ctx, []transfer{{item, archive}}, "下载 Temurin "+label, status)
	}
	if temurinErr == nil {
		target = e.temurinHome(major)
	} else {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		status("Temurin 下载暂不可用，正在切换到 Azul Zulu…")
		name, item, err = azulDownload(ctx, major)
		if err != nil {
			return "", errors.Join(temurinErr, err)
		}
		archive = filepath.Join(stage, name)
		if err := ensureAll(ctx, []transfer{{item, archive}}, "下载 Zulu "+label, status); err != nil {
			return "", errors.Join(temurinErr, err)
		}
	}
	status("正在解压 " + label + "…")
	unpacked := filepath.Join(stage, "extracted")
	if err := extractJDK(ctx, archive, unpacked); err != nil {
		return "", fmt.Errorf("%s 解压失败：%w", label, err)
	}
	status("正在验证并安装 " + label + "…")
	folders, err := os.ReadDir(unpacked)
	if err != nil {
		return "", err
	}
	if len(folders) != 1 || !folders[0].IsDir() || !isJDK(filepath.Join(unpacked, folders[0].Name()), major) {
		return "", fmt.Errorf("%s 压缩包缺少所需版本的 Java 或 javac", label)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	previous := filepath.Join(stage, "previous")
	restore := false
	if err := renameJDKPath(ctx, target, previous); err == nil {
		restore = true
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := renameJDKPath(ctx, filepath.Join(unpacked, folders[0].Name()), target); err != nil {
		if restore {
			if restoreErr := renameJDKPath(context.WithoutCancel(ctx), previous, target); restoreErr != nil {
				cleanup = false
				return "", fmt.Errorf("安装 %s 失败，旧目录保存在 %s：%w", label, previous, err)
			}
		}
		return "", err
	}
	status("已安装 " + label + "，正在清理临时文件…")
	return target, nil
}

func temurinDownload(ctx context.Context, major int) (string, artifact, error) {
	var builds []struct {
		Binary struct {
			Package struct {
				Name, Link, Checksum string
				Size                 int64
			}
		}
	}
	address := fmt.Sprintf("https://api.adoptium.net/v3/assets/latest/%d/hotspot?architecture=x64&image_type=jdk&os=windows&vendor=eclipse", major)
	metadataCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := catalogJSON(metadataCtx, address, &builds); err != nil {
		return "", artifact{}, err
	}
	for _, build := range builds {
		p := build.Binary.Package
		source, err := url.Parse(p.Link)
		hash, hashErr := hex.DecodeString(p.Checksum)
		prefix := fmt.Sprintf("OpenJDK%dU-jdk_x64_windows_hotspot_", major)
		if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil ||
			!strings.HasPrefix(source.Path, fmt.Sprintf("/adoptium/temurin%d-binaries/releases/download/", major)) ||
			!segment(p.Name) || !strings.HasPrefix(p.Name, prefix) || !strings.HasSuffix(p.Name, ".zip") ||
			filepath.Base(source.Path) != p.Name || hashErr != nil || len(hash) != 32 || p.Size <= 0 {
			continue
		}
		relative := fmt.Sprintf("/%d/jdk/x64/windows/%s", major, url.PathEscape(p.Name))
		return p.Name, artifact{URL: p.Link, SHA256: p.Checksum, Size: p.Size, rangeParts: 8,
			Mirrors: []string{"https://mirrors.nju.edu.cn/adoptium" + relative, "https://mirrors.lzu.edu.cn/adoptium" + relative}}, nil
	}
	return "", artifact{}, fmt.Errorf("Adoptium 未返回可校验的 Windows x64 JDK %d", major)
}

func azulDownload(ctx context.Context, major int) (string, artifact, error) {
	query := fmt.Sprintf("java_version=%d&os=windows&arch=x86&hw_bitness=64", major) +
		"&archive_type=zip&java_package_type=jdk&javafx_bundled=false&crac_supported=false" +
		"&release_status=ga&availability_types=CA&latest=true"
	type azulPackage struct {
		ID      string `json:"package_uuid"`
		Name    string
		Version []int `json:"java_version"`
	}
	var packages []azulPackage
	if err := fetchJSON(ctx, azulAPI+"?"+query, &packages); err != nil {
		return "", artifact{}, err
	}
	packages = slices.DeleteFunc(packages, func(p azulPackage) bool {
		return len(p.Version) == 0 || p.Version[0] != major ||
			!segment(p.Name) || !strings.HasSuffix(strings.ToLower(p.Name), "-win_x64.zip") || !packageID.MatchString(p.ID)
	})
	slices.SortFunc(packages, func(a, b azulPackage) int {
		return slices.Compare(b.Version, a.Version)
	})
	if len(packages) == 0 {
		return "", artifact{}, fmt.Errorf("Azul 暂未提供 Windows x64 JDK %d", major)
	}
	var build struct {
		URL  string `json:"download_url"`
		Hash string `json:"sha256_hash"`
	}
	if err := fetchJSON(ctx, azulAPI+packages[0].ID, &build); err != nil {
		return "", artifact{}, err
	}
	source, err := url.Parse(build.URL)
	hash, hashErr := hex.DecodeString(build.Hash)
	if err != nil || source.Scheme != "https" || source.Host != "cdn.azul.com" || hashErr != nil || len(hash) != 32 {
		return "", artifact{}, fmt.Errorf("Azul 返回的 JDK %d 下载地址或校验值无效", major)
	}
	return strings.ToLower(packages[0].Name), artifact{URL: build.URL, SHA256: build.Hash, rangeParts: 8}, nil
}

func normalizeDirectoryCase(ctx context.Context, target string) error {
	parent, name := filepath.Dir(target), filepath.Base(target)
	entries, err := os.ReadDir(parent)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != name && strings.EqualFold(entry.Name(), name) {
			return renameJDKPath(ctx, filepath.Join(parent, entry.Name()), target)
		}
	}
	return nil
}

func renameJDKPath(ctx context.Context, source, target string) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := os.Rename(source, target)
		if attempt == 10 || (!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
			return err
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func extractJDK(ctx context.Context, archive, destination string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	seen := make(map[string]bool, len(z.File))
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, err := localPath(destination, strings.TrimSuffix(entry.Name, "/"))
		key := strings.ToLower(target)
		if err != nil || seen[key] {
			return fmt.Errorf("无效或重复的 Java 压缩条目 %q", entry.Name)
		}
		seen[key] = true
		if entry.Mode().IsDir() {
			err = os.MkdirAll(target, 0700)
		} else {
			err = writeZipEntry(ctx, entry, target)
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

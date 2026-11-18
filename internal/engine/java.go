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

var packageID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func (e Engine) javaHome(major int) string {
	return filepath.Join(e.javaRoot(), fmt.Sprintf("zulu %d", major))
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
	entries, _ := os.ReadDir(e.javaRoot())
	var versions []int
	for _, entry := range entries {
		major, found := strings.CutPrefix(strings.ToLower(entry.Name()), "zulu ")
		n, err := strconv.Atoi(major)
		if found && err == nil && n > 0 && isJDK(e.javaHome(n), n) {
			versions = append(versions, n)
		}
	}
	slices.Sort(versions)
	return versions
}

func (e Engine) EnsureJava(ctx context.Context, major int, status func(string)) (string, error) {
	ctx = e.resumableDownloads(ctx)
	target := e.javaHome(major)
	parent := filepath.Dir(target)
	if err := normalizeDirectoryCase(ctx, parent); err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(parent, fmt.Sprintf(".zulu-%d.lock", major)), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := waitFileLock(ctx, lock); err != nil {
		return "", err
	}
	if isJDK(target, major) {
		if err := normalizeDirectoryCase(ctx, target); err != nil {
			return "", err
		}
		return target, nil
	}
	label := fmt.Sprintf("Zulu JDK %d", major)
	status("正在查询 " + label + "…")
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
		return "", err
	}
	packages = slices.DeleteFunc(packages, func(p azulPackage) bool {
		return len(p.Version) == 0 || p.Version[0] != major ||
			!segment(p.Name) || !strings.HasSuffix(strings.ToLower(p.Name), "-win_x64.zip") || !packageID.MatchString(p.ID)
	})
	slices.SortFunc(packages, func(a, b azulPackage) int {
		return slices.Compare(b.Version, a.Version)
	})
	if len(packages) == 0 {
		return "", fmt.Errorf("Azul 暂未提供 Windows x64 的 %s", label)
	}
	var build struct {
		URL  string `json:"download_url"`
		Hash string `json:"sha256_hash"`
	}
	if err := fetchJSON(ctx, azulAPI+packages[0].ID, &build); err != nil {
		return "", err
	}
	source, err := url.Parse(build.URL)
	hash, hashErr := hex.DecodeString(build.Hash)
	if err != nil || source.Scheme != "https" || source.Host != "cdn.azul.com" || hashErr != nil || len(hash) != 32 {
		return "", fmt.Errorf("Azul 返回的 %s 下载地址或校验值无效", label)
	}

	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stage)
		}
	}()

	archive := filepath.Join(stage, strings.ToLower(packages[0].Name))
	item := transfer{artifact{URL: build.URL, SHA256: build.Hash}, archive}
	progress := &downloadProgress{label: label, status: status, start: time.Now()}
	status("正在下载 " + label + "…")
	if err := item.ensureWithProgress(ctx, progress.add); err != nil {
		return "", err
	}
	status("正在解压 " + label + "…")
	unpacked := filepath.Join(stage, "extracted")
	if err := extractJDK(ctx, archive, unpacked); err != nil {
		return "", fmt.Errorf("%s 解压失败：%w", label, err)
	}
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
	return target, nil
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

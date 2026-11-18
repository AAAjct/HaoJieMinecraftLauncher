package engine

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	azulAPI = "https://api.azul.com/metadata/v1/zulu/packages/"
)

var packageID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func (e Engine) javaHome(major int) string {
	return filepath.Join(e.DataDir, "java", fmt.Sprintf("zulu-%d", major))
}

func isJDK(home string) bool {
	return regularFile(filepath.Join(home, "bin", "java.exe")) &&
		regularFile(filepath.Join(home, "bin", "javac.exe"))
}

func (e Engine) InstalledJava() []int {
	entries, _ := os.ReadDir(filepath.Join(e.DataDir, "java"))
	var versions []int
	for _, entry := range entries {
		major, found := strings.CutPrefix(entry.Name(), "zulu-")
		n, err := strconv.Atoi(major)
		if found && err == nil && n > 0 && isJDK(e.javaHome(n)) {
			versions = append(versions, n)
		}
	}
	slices.Sort(versions)
	return versions
}

func (e Engine) EnsureJava(ctx context.Context, major int, status func(string)) (string, error) {
	target := e.javaHome(major)
	if isJDK(target) {
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
			!strings.HasSuffix(p.Name, "-win_x64.zip") || !packageID.MatchString(p.ID)
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

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	archive := filepath.Join(stage, "jdk.zip")
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
	if len(folders) != 1 || !folders[0].IsDir() || !isJDK(filepath.Join(unpacked, folders[0].Name())) {
		return "", fmt.Errorf("%s 压缩包缺少 Java 或 javac", label)
	}
	if err := os.Rename(filepath.Join(unpacked, folders[0].Name()), target); err != nil && !isJDK(target) {
		return "", err
	}
	return target, nil
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
			err = writeZipEntry(entry, target)
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

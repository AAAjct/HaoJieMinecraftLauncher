package engine

import (
	"archive/zip"
	"cmp"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

type artifact struct {
	URL                 string   `json:"url,omitempty"`
	Path                string   `json:"path,omitempty"`
	Size                int64    `json:"size,omitempty"`
	SHA1                string   `json:"sha1,omitempty"`
	SHA256              string   `json:"sha256,omitempty"`
	SHA512              string   `json:"sha512,omitempty"`
	Mirrors, Alternates []string `json:"-"`
	localSource         string
	rangeParts          int
	publicArchiveFolder string
}

type transfer struct {
	artifact
	target string
}

func localPath(root, relative string) (string, error) {
	path, err := filepath.Localize(relative)
	if err != nil || relative == "." {
		return "", fmt.Errorf("无效文件路径 %q", relative)
	}
	return filepath.Join(root, path), nil
}

func segment(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\") && s != "." && filepath.IsLocal(s)
}

func (t transfer) digest() (hash.Hash, string) {
	switch {
	case t.SHA512 != "":
		return sha512.New(), t.SHA512
	case t.SHA256 != "":
		return sha256.New(), t.SHA256
	case t.SHA1 != "":
		return sha1.New(), t.SHA1
	default:
		return nil, ""
	}
}

func (t transfer) ready() bool {
	info, err := os.Stat(t.target)
	return err == nil && info.Mode().IsRegular() && (info.Size() > 0 || t.SHA1 == "da39a3ee5e6b4b0d3255bfef95601890afd80709") &&
		(t.Size <= 0 || info.Size() == t.Size)
}

func (t transfer) ensure(ctx context.Context) error {
	return t.ensureWithProgress(ctx, nil)
}

var ErrTaskCanceled = errors.New("任务已取消")

func (t transfer) ensureWithProgress(ctx context.Context, progress func(int64, int64)) (result error) {
	if t.ready() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.target), 0700); err != nil {
		return err
	}
	if t.localSource != "" {
		input, err := os.Open(t.localSource)
		if err == nil {
			defer input.Close()
			err = t.saveTransfer(ctx, func(out *os.File, digest hash.Hash, limit int64) (int64, error) {
				return io.Copy(io.MultiWriter(out, digest), io.LimitReader(cancellableReader{ctx, input}, limit+1))
			})
		}
		if err != nil {
			return fmt.Errorf("整合包旁的 %s 校验或复制失败：%w", filepath.Base(t.localSource), err)
		}
		return nil
	}
	cache, lock, err := t.lockResumeCache(ctx)
	if err != nil {
		return err
	}
	if lock != nil {
		defer lock.Close()
		defer func() {
			if err := os.RemoveAll(cache); err != nil {
				result = errors.Join(result, fmt.Errorf("无法清理下载缓存：%w", err))
			}
		}()
		if t.ready() {
			return nil
		}
	}
	sources := slices.Compact(t.orderedSources())
	failures := make([]error, len(sources))
	permanent := make([]bool, len(sources))
	for attempt := range 3 {
		retry := false
		for i, source := range sources {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if permanent[i] {
				continue
			}
			t.URL = source
			if cache != "" {
				workers := t.rangeParts
				if attempt == 2 {
					workers = 1
				} else if workers > 4 && attempt == 1 {
					workers = 4
				}
				err = t.copyResumable(ctx, cache, workers, progress)
			} else {
				err = t.copyWithProgress(ctx, progress)
			}
			if err == nil {
				return nil
			}
			var failure httpFailure
			if errors.As(err, &failure) {
				permanent[i] = failure.status == http.StatusNotFound || failure.status == http.StatusGone ||
					failure.status == http.StatusForbidden || failure.status == http.StatusUnauthorized
			} else if u, parseErr := url.Parse(source); parseErr == nil && u.Host != "" {
				var requestError *url.Error
				if errors.As(err, &requestError) {
					err = requestError.Err
				}
				err = fmt.Errorf("%s：%w", u.Host, err)
			}
			failures[i] = err
			retry = retry || !permanent[i]
		}
		if !retry {
			break
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("%s：%w", filepath.Base(t.target), errors.Join(failures...))
}

func (t transfer) copy(ctx context.Context) error {
	return t.copyWithProgress(ctx, nil)
}

func (t transfer) copyWithProgress(ctx context.Context, progress func(int64, int64)) error {
	res, err := getTransfer(ctx, t.URL, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if t.publicArchiveFolder != "" && res.Request.URL.String() != t.URL {
		return fmt.Errorf("CurseForge CDN 返回了非预期的下载地址")
	}
	return t.saveTransfer(ctx, func(file *os.File, digest hash.Hash, limit int64) (int64, error) {
		if t.publicArchiveFolder != "" {
			return copyHTTPBody(res, file, digest, limit, progress)
		}
		return copyHTTP(ctx, t.URL, res, file, digest, limit, progress)
	})
}

var errTransferHash = errors.New("文件哈希校验失败")

func (t transfer) sizeLimit() int64 {
	if t.Size > 0 {
		return t.Size
	}
	return 512 << 20
}

func (t transfer) saveTransfer(ctx context.Context, write func(*os.File, hash.Hash, int64) (int64, error)) error {
	dir := downloadTempDir(ctx)
	if dir == "" {
		dir = filepath.Dir(t.target)
	} else if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	limit := t.sizeLimit()
	digest, expected := t.digest()
	n, err := write(file, digest, limit)
	if err != nil {
		return err
	}
	if (n == 0 && digest == nil) || n > limit || (t.Size > 0 && n != t.Size) {
		return fmt.Errorf("文件大小校验失败")
	}
	if digest != nil && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected) {
		return errTransferHash
	}
	if t.publicArchiveFolder != "" {
		if err := validateCurseArchive(file, n, t.publicArchiveFolder); err != nil {
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := moveDownload(file.Name(), t.target); err != nil && !t.ready() {
		return err
	}
	return nil
}

func validateCurseArchive(file *os.File, size int64, folder string) error {
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return fmt.Errorf("CurseForge CDN 返回的文件不是有效的 ZIP 或 JAR：%w", err)
	}
	for _, entry := range archive.File {
		if folder == "mods" && entry.Name == "META-INF/MANIFEST.MF" ||
			folder == "resourcepacks" && entry.Name == "pack.mcmeta" ||
			folder == "shaderpacks" && strings.HasPrefix(entry.Name, "shaders/") && entry.Mode().IsRegular() {
			return nil
		}
	}
	return fmt.Errorf("CurseForge CDN 返回的压缩包与资源类型不匹配")
}

func moveDownload(source, target string) error {
	if strings.EqualFold(filepath.VolumeName(source), filepath.VolumeName(target)) {
		return os.Rename(source, target)
	}
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_COPY_ALLOWED|windows.MOVEFILE_REPLACE_EXISTING)
}

func linkOrCopy(source, target string) error {
	if strings.EqualFold(filepath.VolumeName(source), filepath.VolumeName(target)) {
		return os.Link(source, target)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	if err = errors.Join(err, output.Close()); err != nil {
		_ = os.Remove(target)
	}
	return err
}

func ensureAll(ctx context.Context, work []transfer, label string, status func(string)) error {
	return ensureBatch(ctx, work, label, status, true)
}

func ensureBatch(ctx context.Context, work []transfer, label string, status func(string), stopOnError bool) error {
	total := len(work)
	var pending []transfer
	var done int
	var totalSize, downloaded int64
	knownSize := true
	for _, item := range work {
		if err := ctx.Err(); err != nil {
			return err
		}
		totalSize += item.Size
		knownSize = knownSize && item.Size > 0
		if item.ready() {
			done++
			downloaded += item.Size
		} else {
			pending = append(pending, item)
		}
	}
	slices.SortStableFunc(pending, func(a, b transfer) int { return cmp.Compare(b.Size, a.Size) })
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var next atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	var received, lastBytes int64
	last := time.Now()
	emit := func() {
		now := time.Now()
		rate := float64(received-lastBytes) / (1 << 20) / max(now.Sub(last).Seconds(), 0.1)
		fraction := 1.0
		if done < total {
			fraction = float64(done) / float64(total)
			if knownSize && totalSize > 0 {
				fraction = float64(downloaded) / float64(totalSize)
			}
		} else {
			rate = 0
		}
		reportStage(ctx, StageProgress{Label: label, Fraction: fraction, Remaining: total - done, BytesPerSecond: rate * (1 << 20)})
		message := fmt.Sprintf("%s · %d/%d 文件", label, done, total)
		if knownSize && totalSize > 0 {
			message += fmt.Sprintf(" · %.1f%%（%.2f/%.2f MiB）", 100*float64(downloaded)/float64(totalSize), float64(downloaded)/(1<<20), float64(totalSize)/(1<<20))
		} else {
			message += fmt.Sprintf(" · 已接收 %.2f MiB", float64(received)/(1<<20))
		}
		status(fmt.Sprintf("%s · %.1f MiB/s", message, rate))
		last, lastBytes = now, received
	}
	emit()
	if len(pending) == 0 {
		return ctx.Err()
	}
	for range min(32, len(pending)) {
		wg.Go(func() {
			for ctx.Err() == nil {
				index := int(next.Add(1)) - 1
				if index >= len(pending) {
					return
				}
				item := pending[index]
				var credited int64
				size := item.Size
				err := item.ensureWithProgress(ctx, func(n, responseSize int64) {
					mu.Lock()
					if total == 1 && size <= 0 && responseSize > 0 {
						size, totalSize, knownSize = responseSize, responseSize, true
					}
					firstBytes := n > 0 && received == 0
					before := credited
					if n < 0 {
						credited = 0
					} else {
						received += n
						credited = min(size, credited+n)
					}
					downloaded += credited - before
					if firstBytes || time.Since(last) >= 250*time.Millisecond {
						emit()
					}
					mu.Unlock()
				})
				mu.Lock()
				if err == nil {
					done++
					downloaded += size - credited
				} else if first == nil {
					first = err
					if stopOnError {
						cancel()
					}
				}
				if time.Since(last) >= 250*time.Millisecond || done == total {
					emit()
				}
				mu.Unlock()
			}
		})
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			if first != nil {
				return first
			}
			return ctx.Err()
		case <-ticker.C:
			mu.Lock()
			if done < total && time.Since(last) >= time.Second {
				emit()
			}
			mu.Unlock()
		}
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

func writeFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func readJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(value)
}

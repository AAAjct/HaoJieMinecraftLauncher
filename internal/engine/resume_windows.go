package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const minimumResumableSize = 1 << 20

type downloadCacheKey struct{}

func (e Engine) cacheDir() string {
	return filepath.Join(e.gameRoot(), ".hjml-cache")
}

func (e Engine) MigrateDownloadCache() error {
	old := filepath.Join(e.gameRoot(), ".hjm-cache")
	entries, err := os.ReadDir(old)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	root := e.cacheDir()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	var preserved string
	for _, entry := range entries {
		target := filepath.Join(root, entry.Name())
		if _, err := os.Lstat(target); err == nil {
			if preserved == "" {
				preserved, err = os.MkdirTemp(root, ".legacy-")
				if err != nil {
					return err
				}
			}
			target = filepath.Join(preserved, entry.Name())
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(filepath.Join(old, entry.Name()), target); err != nil {
			return err
		}
	}
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (e Engine) resumableDownloads(ctx context.Context) context.Context {
	if ctx.Value(downloadCacheKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, downloadCacheKey{}, filepath.Join(e.cacheDir(), "temp"))
}

func downloadTempDir(ctx context.Context) string {
	dir, _ := ctx.Value(downloadCacheKey{}).(string)
	return dir
}

func makeCacheStage(ctx context.Context, pattern string) (string, error) {
	dir := downloadTempDir(ctx)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, pattern)
}

func (t transfer) lockResumeCache(ctx context.Context) (string, *os.File, error) {
	root := downloadTempDir(ctx)
	_, checksum := t.digest()
	if root == "" || checksum == "" || t.Size > 0 && t.Size < minimumResumableSize {
		return "", nil, nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%s/%d", strings.ToLower(checksum), t.Size)))
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, nil
	}
	locks := filepath.Join(filepath.Dir(root), "locks")
	if err := os.MkdirAll(locks, 0700); err != nil {
		return "", nil, nil
	}
	lock, err := os.OpenFile(filepath.Join(locks, key[:2]+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, nil
	}
	if _, err := waitFileLock(ctx, lock, nil); err != nil {
		lock.Close()
		return "", nil, err
	}
	return filepath.Join(root, key), lock, nil
}

// waitFileLock returns false without acquiring the lock if ready becomes true
// while another caller holds it.
func waitFileLock(ctx context.Context, lock *os.File, ready func() bool) (bool, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var position windows.Overlapped
		err := windows.LockFileEx(windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &position)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, err
		}
		if ready != nil && ready() {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

type resumePlan struct {
	Size  int64
	Parts int
}

var (
	errResumeRange = errors.New("下载源不支持所请求的断点")
	errResumeState = errors.New("断点文件与预期大小不符")
)

func (t transfer) copyResumable(ctx context.Context, dir string, workers int, progress func(int64, int64)) error {
	meta := filepath.Join(dir, "download.json")
	var plan resumePlan
	var initial *http.Response
	if readJSON(meta, &plan) != nil || plan.Size < minimumResumableSize || plan.Size > t.sizeLimit() ||
		(t.Size > 0 && plan.Size != t.Size) || (plan.Parts != 1 && plan.Parts != 4 && plan.Parts != 8) {
		var err error
		initial, err = getTransfer(ctx, t.URL, "")
		if err != nil {
			return err
		}
		defer initial.Body.Close()
		size := initial.ContentLength
		if size < 0 {
			size = t.Size
		}
		if size < minimumResumableSize {
			return t.saveTransfer(ctx, func(file *os.File, digest hash.Hash, limit int64) (int64, error) {
				return copyHTTP(ctx, t.URL, initial, file, digest, limit, progress)
			})
		}
		if size > t.sizeLimit() || t.Size > 0 && size != t.Size {
			return fmt.Errorf("文件大小校验失败")
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		plan = resumePlan{Size: size, Parts: 1}
		if workers != 1 && size >= minimumSegmentedSize && strings.EqualFold(initial.Header.Get("Accept-Ranges"), "bytes") {
			plan.Parts = max(4, t.rangeParts)
		}
		if err := writeJSON(meta, plan); err != nil {
			return err
		}
	}
	if workers == 1 && plan.Parts > 1 {
		plan.Parts = 1
		if err := writeJSON(meta, plan); err != nil {
			return err
		}
		if progress != nil {
			progress(-1, plan.Size)
		}
	}
	if plan.Parts > 1 {
		if initial != nil {
			initial.Body.Close()
		}
		err := t.resumeRanges(ctx, dir, plan, workers, progress)
		if errors.Is(err, errResumeRange) {
			plan.Parts = 1
			if err := writeJSON(meta, plan); err != nil {
				return err
			}
			if progress != nil {
				progress(-1, plan.Size)
			}
			initial = nil
		} else if err != nil {
			if errors.Is(err, errResumeState) {
				_ = os.RemoveAll(dir)
			}
			return err
		}
	}
	if plan.Parts == 1 {
		if err := t.resumePart(ctx, filepath.Join(dir, "full"), plan.Size, 0, plan.Size, initial, progress); err != nil {
			if errors.Is(err, errResumeState) {
				_ = os.RemoveAll(dir)
			}
			return err
		}
	}
	err := t.saveTransfer(ctx, func(file *os.File, digest hash.Hash, _ int64) (int64, error) {
		out := io.MultiWriter(file, digest)
		var copied int64
		for part := range plan.Parts {
			if err := ctx.Err(); err != nil {
				return copied, err
			}
			name := "full"
			if plan.Parts > 1 {
				name = fmt.Sprint(part)
			}
			source, err := os.Open(filepath.Join(dir, name))
			if err != nil {
				return copied, err
			}
			n, err := io.Copy(out, source)
			source.Close()
			copied += n
			if err != nil {
				return copied, err
			}
		}
		return copied, nil
	})
	if errors.Is(err, errTransferHash) {
		_ = os.RemoveAll(dir)
	}
	return err
}

func (t transfer) resumeRanges(ctx context.Context, dir string, plan resumePlan, workers int, progress func(int64, int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	if workers <= 0 {
		workers = plan.Parts
	}
	active := make(chan struct{}, workers)
	for part := range plan.Parts {
		start := plan.Size * int64(part) / int64(plan.Parts)
		end := plan.Size * int64(part+1) / int64(plan.Parts)
		wg.Go(func() {
			select {
			case active <- struct{}{}:
				defer func() { <-active }()
			case <-ctx.Done():
				return
			}
			err := t.resumePart(ctx, filepath.Join(dir, fmt.Sprint(part)), plan.Size, start, end, nil, progress)
			if err != nil {
				once.Do(func() { first = err; cancel() })
			}
		})
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

func (t transfer) resumePart(ctx context.Context, path string, total, start, end int64, initial *http.Response, progress func(int64, int64)) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	existing, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if existing == end-start {
		return nil
	}
	if existing > end-start {
		return errResumeState
	}

	partial := start != 0 || end != total
	res := initial
	if partial || existing > 0 {
		rangeHeader := fmt.Sprintf("bytes=%d-%d", start+existing, end-1)
		res, err = getTransfer(ctx, t.URL, rangeHeader)
		if failure, ok := err.(httpFailure); ok && rangeRejected(failure.status) {
			if partial {
				return errResumeRange
			}
			res, err = getTransfer(ctx, t.URL, "")
		}
	} else if res == nil {
		res, err = getTransfer(ctx, t.URL, "")
	}
	if err != nil {
		return err
	}
	defer func() { res.Body.Close() }()
	if res.StatusCode == http.StatusPartialContent {
		want := fmt.Sprintf("bytes %d-%d/%d", start+existing, end-1, total)
		if res.Header.Get("Content-Range") != want {
			if partial {
				return errResumeRange
			}
			res.Body.Close()
			full, err := getTransfer(ctx, t.URL, "")
			if err != nil {
				return err
			}
			res = full
		}
	}
	if res.StatusCode == http.StatusOK {
		if partial {
			return errResumeRange
		}
		if res.ContentLength >= 0 && res.ContentLength != total {
			return errResumeState
		}
		if existing > 0 {
			if err := file.Truncate(0); err != nil {
				return err
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			existing = 0
			if progress != nil {
				progress(-1, total)
			}
		}
	}
	var out io.Writer = file
	if progress != nil {
		out = progressWriter{file, total, progress}
	}
	_, err = io.CopyN(out, res.Body, end-start-existing)
	return err
}

func rangeRejected(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed,
		http.StatusRequestedRangeNotSatisfiable, http.StatusNotImplemented:
		return true
	}
	return false
}

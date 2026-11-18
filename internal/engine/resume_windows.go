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

func (e Engine) resumableDownloads(ctx context.Context) context.Context {
	if ctx.Value(downloadCacheKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, downloadCacheKey{}, filepath.Join(e.gameRoot(), ".hjml-cache", "downloads"))
}

func (t transfer) lockResumeCache(ctx context.Context) (string, *os.File, error) {
	root, _ := ctx.Value(downloadCacheKey{}).(string)
	_, checksum := t.digest()
	if root == "" || checksum == "" || t.Size > 0 && t.Size < minimumResumableSize {
		return "", nil, nil
	}
	key := fmt.Sprintf("%x", sha256.Sum256(fmt.Appendf(nil, "%s/%d", strings.ToLower(checksum), t.Size)))
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", nil, nil
	}
	lock, err := os.OpenFile(filepath.Join(root, key[:2]+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var position windows.Overlapped
		err := windows.LockFileEx(windows.Handle(lock.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &position)
		if err == nil {
			return filepath.Join(root, key), lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			lock.Close()
			return "", nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return "", nil, ctx.Err()
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

func (t transfer) copyResumable(ctx context.Context, dir string, progress func(int64, int64)) error {
	meta := filepath.Join(dir, "download.json")
	var plan resumePlan
	var initial *http.Response
	if readJSON(meta, &plan) != nil || plan.Size < minimumResumableSize || plan.Size > t.sizeLimit() ||
		(t.Size > 0 && plan.Size != t.Size) || (plan.Parts != 1 && plan.Parts != 4) {
		var err error
		initial, err = get(ctx, t.URL)
		if err != nil {
			return err
		}
		defer initial.Body.Close()
		size := initial.ContentLength
		if size < 0 {
			size = t.Size
		}
		if size < minimumResumableSize {
			return t.saveTransfer(func(file *os.File, digest hash.Hash, limit int64) (int64, error) {
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
		if size >= minimumSegmentedSize && strings.EqualFold(initial.Header.Get("Accept-Ranges"), "bytes") {
			plan.Parts = 4
		}
		if err := writeJSON(meta, plan); err != nil {
			return err
		}
	}
	if plan.Parts == 4 {
		if initial != nil {
			initial.Body.Close()
		}
		err := t.resumeRanges(ctx, dir, plan, progress)
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
	err := t.saveTransfer(func(file *os.File, digest hash.Hash, _ int64) (int64, error) {
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
	if err == nil || errors.Is(err, errTransferHash) {
		_ = os.RemoveAll(dir)
	}
	return err
}

func (t transfer) resumeRanges(ctx context.Context, dir string, plan resumePlan, progress func(int64, int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for part := range plan.Parts {
		start := plan.Size * int64(part) / int64(plan.Parts)
		end := plan.Size * int64(part+1) / int64(plan.Parts)
		wg.Go(func() {
			err := t.resumePart(ctx, filepath.Join(dir, fmt.Sprint(part)), plan.Size, start, end, nil, progress)
			if err != nil {
				once.Do(func() { first = err; cancel() })
			}
		})
	}
	wg.Wait()
	return first
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
		res, err = getHTTP(ctx, t.URL, rangeHeader)
		if failure, ok := err.(httpFailure); ok && (failure.status == http.StatusBadRequest || failure.status == http.StatusRequestedRangeNotSatisfiable) {
			if partial {
				return errResumeRange
			}
			res, err = get(ctx, t.URL)
		}
	} else if res == nil {
		res, err = get(ctx, t.URL)
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
			full, err := get(ctx, t.URL)
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

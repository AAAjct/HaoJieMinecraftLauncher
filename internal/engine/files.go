package engine

import (
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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type artifact struct {
	URL     string   `json:"url,omitempty"`
	Path    string   `json:"path,omitempty"`
	Size    int64    `json:"size,omitempty"`
	SHA1    string   `json:"sha1,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	SHA512  string   `json:"sha512,omitempty"`
	Mirrors []string `json:"-"`
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
	cache, lock, err := t.lockResumeCache(ctx)
	if err != nil {
		return err
	}
	if lock != nil {
		defer lock.Close()
		defer func() {
			if errors.Is(context.Cause(ctx), ErrTaskCanceled) {
				if err := os.RemoveAll(cache); err != nil {
					result = errors.Join(result, fmt.Errorf("无法清理下载缓存：%w", err))
				}
			}
		}()
		if t.ready() {
			return nil
		}
	}
	sources := t.orderedSources(ctx)
	for attempt := range 3 {
		for _, source := range sources {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			t.URL = source
			downloadCtx, cancel := ctx, func() {}
			if len(sources) > 1 && t.Size > 0 && t.Size <= 1<<20 {
				downloadCtx, cancel = context.WithTimeout(ctx, 8*time.Second)
			}
			if cache != "" {
				err = t.copyResumable(downloadCtx, cache, progress)
			} else {
				err = t.copyWithProgress(downloadCtx, progress)
			}
			cancel()
			if err == nil {
				return nil
			}
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
	return fmt.Errorf("%s：%w", filepath.Base(t.target), err)
}

func (t transfer) copy(ctx context.Context) error {
	return t.copyWithProgress(ctx, nil)
}

func (t transfer) copyWithProgress(ctx context.Context, progress func(int64, int64)) error {
	res, err := get(ctx, t.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return t.saveTransfer(func(file *os.File, digest hash.Hash, limit int64) (int64, error) {
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

func (t transfer) saveTransfer(write func(*os.File, hash.Hash, int64) (int64, error)) error {
	file, err := os.CreateTemp(filepath.Dir(t.target), ".part-*")
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
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), t.target); err != nil && !t.ready() {
		return err
	}
	return nil
}

func ensureAll(ctx context.Context, work []transfer, label string, status func(string)) error {
	ctx = withSourcePreferences(ctx)
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
	for range min(32, len(pending)) {
		wg.Go(func() {
			for ctx.Err() == nil {
				index := int(next.Add(1)) - 1
				if index >= len(pending) {
					return
				}
				item := pending[index]
				var credited int64
				err := item.ensureWithProgress(ctx, func(n, _ int64) {
					mu.Lock()
					firstBytes := n > 0 && received == 0
					before := credited
					if n < 0 {
						credited = 0
					} else {
						received += n
						credited = min(item.Size, credited+n)
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
					downloaded += item.Size - credited
				} else if first == nil {
					first = err
					cancel()
				}
				if time.Since(last) >= 250*time.Millisecond || done == total {
					emit()
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
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

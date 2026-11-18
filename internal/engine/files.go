package engine

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
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

func (t transfer) ensureWithProgress(ctx context.Context, progress func(int64, int64)) error {
	if t.ready() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.target), 0700); err != nil {
		return err
	}
	var err error
	for _, source := range append([]string{t.URL}, t.Mirrors...) {
		t.URL = source
		for attempt := range 3 {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			err = t.copyWithProgress(ctx, progress)
			if err == nil {
				return nil
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
	file, err := os.CreateTemp(filepath.Dir(t.target), ".part-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	limit := int64(512 << 20)
	if t.Size > 0 {
		limit = t.Size
	}
	digest, expected := t.digest()
	n, err := copyHTTP(ctx, t.URL, res, file, digest, limit, progress)
	if err != nil {
		return err
	}
	if (n == 0 && digest == nil) || n > limit || (t.Size > 0 && n != t.Size) {
		return fmt.Errorf("文件大小校验失败")
	}
	if digest != nil && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected) {
		return fmt.Errorf("文件哈希校验失败")
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	status(fmt.Sprintf("%s · 0/%d", label, len(work)))
	var next atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	var done int
	var last time.Time
	var totalBytes atomic.Int64
	start := time.Now()
	emit := func() {
		rate := float64(totalBytes.Load()) / (1 << 20) / max(time.Since(start).Seconds(), 0.1)
		status(fmt.Sprintf("%s · %d/%d · %.1f MiB/s", label, done, len(work), rate))
		last = time.Now()
	}
	newBytes := func(n, _ int64) {
		if n > 0 {
			totalBytes.Add(n)
		}
	}
	for range min(32, len(work)) {
		wg.Go(func() {
			for ctx.Err() == nil {
				index := int(next.Add(1)) - 1
				if index >= len(work) {
					return
				}
				err := work[index].ensureWithProgress(ctx, newBytes)
				mu.Lock()
				if err != nil && first == nil {
					first = err
					cancel()
				}
				done++
				if time.Since(last) > 250*time.Millisecond || done == len(work) {
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

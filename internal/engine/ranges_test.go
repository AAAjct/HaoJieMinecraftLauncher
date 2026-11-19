package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelRangeAndFallback(t *testing.T) {
	content := bytes.Repeat([]byte("hjml download content"), (minimumSegmentedSize/21)+4)
	for _, ranges := range []bool{true, false} {
		t.Run(fmt.Sprint(ranges), func(t *testing.T) {
			var segments atomic.Int32
			var active atomic.Int32
			var maximum atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("ETag", `"stable"`)
				if r.Header.Get("Range") != "" {
					segments.Add(1)
					if !ranges {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					n := active.Add(1)
					defer active.Add(-1)
					for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
					}
					time.Sleep(20 * time.Millisecond)
				}
				http.ServeContent(w, r, "file", time.Time{}, bytes.NewReader(content))
			}))
			defer server.Close()
			target := filepath.Join(t.TempDir(), "large.bin")
			work := transfer{artifact{URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), Size: int64(len(content))}, target}
			cache := Engine{DataDir: t.TempDir()}
			if err := work.ensure(cache.resumableDownloads(t.Context())); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, content) {
				t.Fatalf("output mismatch: %v", err)
			}
			if segments.Load() == 0 || (ranges && (segments.Load() != 4 || maximum.Load() < 2)) {
				t.Fatalf("ranges: %d, simultaneous: %d", segments.Load(), maximum.Load())
			}
		})
	}
}

func TestResumeAcrossDownloadAttempts(t *testing.T) {
	for _, parts := range []int64{1, 4} {
		t.Run(strconv.FormatInt(parts, 10), func(t *testing.T) {
			size := int64(2 << 20)
			if parts == 4 {
				size = minimumSegmentedSize
			}
			content := bytes.Repeat([]byte{1, 2, 3, 4}, int(size)/4)
			const block = 64 << 10
			var stopped atomic.Bool
			var resumed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start, end := int64(0), size-1
				if parts == 4 {
					w.Header().Set("Accept-Ranges", "bytes")
				}
				if header := r.Header.Get("Range"); header != "" {
					if _, err := fmt.Sscanf(header, "bytes=%d-%d", &start, &end); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
					w.WriteHeader(http.StatusPartialContent)
				} else {
					w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
				}
				if stopped.Load() {
					if start%(size/parts) != 0 {
						resumed.Add(1)
					}
					_, _ = w.Write(content[start : end+1])
					return
				}
				_, _ = w.Write(content[start : start+block])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()

			e := Engine{DataDir: t.TempDir()}
			ctx, cancel := context.WithTimeout(e.resumableDownloads(t.Context()), 10*time.Second)
			defer cancel()
			file := artifact{URL: server.URL, Size: size, SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}
			first := transfer{file, filepath.Join(t.TempDir(), "unfinished.bin")}
			var received atomic.Int64
			cache, lock, err := first.lockResumeCache(ctx)
			if err != nil || lock == nil {
				t.Fatalf("could not reserve test download cache: %v", err)
			}
			err = first.copyResumable(ctx, cache, func(n, _ int64) {
				if received.Add(n) >= parts*block {
					cancel()
				}
			})
			lock.Close()
			if err == nil || regularFile(first.target) || received.Load() < parts*block {
				t.Fatalf("expected interrupted download after %d bytes: received %d, %v", parts*block, received.Load(), err)
			}
			stopped.Store(true)
			second := transfer{file, filepath.Join(t.TempDir(), "resumed.bin")}
			if err := second.ensure(e.resumableDownloads(t.Context())); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(second.target); err != nil || !bytes.Equal(data, content) {
				t.Fatalf("resumed data differs: %v", err)
			}
			if int64(resumed.Load()) != parts {
				t.Fatalf("resumed %d of %d requests", resumed.Load(), parts)
			}
		})
	}
}

func TestCancelTaskRemovesPartialDownload(t *testing.T) {
	const size = 2 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = w.Write(make([]byte, 64<<10))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	e := Engine{DataDir: t.TempDir()}
	ctx, cancel := context.WithCancelCause(e.resumableDownloads(t.Context()))
	defer cancel(nil)
	targetDir := t.TempDir()
	file := transfer{artifact{URL: server.URL, Size: size, SHA256: fmt.Sprintf("%x", sha256.Sum256(make([]byte, size)))}, filepath.Join(targetDir, "file.zip")}
	err := file.ensureWithProgress(ctx, func(n, _ int64) {
		if n > 0 {
			cancel(ErrTaskCanceled)
		}
	})
	if err == nil || context.Cause(ctx) != ErrTaskCanceled {
		t.Fatalf("expected explicit cancellation, got %v", err)
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("download temporary files remain: %v (%v)", entries, err)
	}
	entries, err = os.ReadDir(filepath.Join(e.DataDir, ".hjm-cache", "temp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial download cache remains: %v", entries)
	}
}

func TestFailedDownloadRemovesPartialCache(t *testing.T) {
	const size = 2 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(size))
		_, _ = w.Write(make([]byte, 64<<10))
		w.(http.Flusher).Flush()
	}))
	defer server.Close()
	e := Engine{GameRoot: t.TempDir()}
	target := filepath.Join(e.GameRoot, "libraries", "file.jar")
	file := transfer{artifact{URL: server.URL, Size: size, SHA256: fmt.Sprintf("%x", sha256.Sum256(make([]byte, size)))}, target}
	if err := file.ensure(e.resumableDownloads(t.Context())); err == nil || regularFile(target) {
		t.Fatalf("incomplete download was published: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(e.GameRoot, ".hjm-cache", "temp"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache after failed download: %v (%v)", entries, err)
	}
}

func TestDownloadProgressBeforeFirstFileCompletes(t *testing.T) {
	content := make([]byte, 512<<10)
	updated := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content[:256<<10])
		w.(http.Flusher).Flush()
		select {
		case <-updated:
			_, _ = w.Write(content[256<<10:])
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	file := transfer{artifact{URL: server.URL, Size: int64(len(content))}, filepath.Join(t.TempDir(), "file")}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := ensureAll(ctx, []transfer{file}, "下载", func(status string) {
		if strings.Contains(status, "0/1 文件") && !strings.Contains(status, " · 0.0%") {
			select {
			case <-updated:
			default:
				close(updated)
			}
		}
	})
	if err != nil {
		t.Fatalf("no byte progress appeared before the file finished: %v", err)
	}
}

func TestDownloadsPreferMirrorAndFallBackAfterHashMismatch(t *testing.T) {
	content := []byte("valid file")
	var officialRequests, mirrorRequests atomic.Int32
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		officialRequests.Add(1)
		_, _ = w.Write(content)
	}))
	defer official.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorRequests.Add(1)
		if r.URL.Path == "/fallback.jar" {
			_, _ = w.Write([]byte("wrong file"))
			return
		}
		_, _ = w.Write(content)
	}))
	defer mirror.Close()
	for _, name := range []string{"mirror.jar", "fallback.jar"} {
		file := artifact{URL: official.URL + "/" + name, Mirrors: []string{mirror.URL + "/" + name},
			Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}
		if err := (transfer{file, filepath.Join(t.TempDir(), name)}).ensure(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if mirrorRequests.Load() != 2 || officialRequests.Load() != 1 {
		t.Fatalf("mirror=%d official=%d, expected two mirror requests and one fallback", mirrorRequests.Load(), officialRequests.Load())
	}
}

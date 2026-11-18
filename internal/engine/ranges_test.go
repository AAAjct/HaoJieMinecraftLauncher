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
			err := first.ensureWithProgress(ctx, func(n, _ int64) {
				if received.Add(n) >= parts*block {
					cancel()
				}
			})
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

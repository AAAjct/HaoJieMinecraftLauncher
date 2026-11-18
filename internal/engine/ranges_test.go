package engine

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			if err := work.ensure(t.Context()); err != nil {
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

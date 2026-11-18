package engine

import (
	"context"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

const minimumSegmentedSize = 16 << 20

// copyHTTP keeps small files on one request. Large files use disjoint writes
// when the server supports ranges, with a fresh single response as fallback.
func copyHTTP(ctx context.Context, source string, response *http.Response, file *os.File, digest hash.Hash, limit int64, progress func(int64, int64)) (int64, error) {
	size := response.ContentLength
	if size > limit {
		return 0, fmt.Errorf("下载文件超过大小限制")
	}
	if size >= minimumSegmentedSize && strings.EqualFold(response.Header.Get("Accept-Ranges"), "bytes") {
		response.Body.Close()
		if err := writeRanges(ctx, source, file, size, response.Header.Get("ETag"), progress); err == nil {
			if digest == nil {
				return size, nil
			}
			return io.Copy(digest, io.NewSectionReader(file, 0, size))
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err := file.Truncate(0); err != nil {
			return 0, err
		}
		if progress != nil {
			progress(-1, size)
		}
		var err error
		response, err = get(ctx, source)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		size = response.ContentLength
	}
	var out io.Writer = file
	if digest != nil {
		out = io.MultiWriter(file, digest)
	}
	if progress != nil {
		out = progressWriter{out, size, progress}
	}
	return io.Copy(out, io.LimitReader(response.Body, limit+1))
}

type progressWriter struct {
	io.Writer
	size     int64
	progress func(int64, int64)
}

func (w progressWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	w.progress(int64(n), w.size)
	return n, err
}

func writeRanges(ctx context.Context, source string, file *os.File, size int64, etag string, progress func(int64, int64)) error {
	if err := file.Truncate(size); err != nil {
		return err
	}
	const parts = 4
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for part := range parts {
		start, end := size*int64(part)/parts, size*int64(part+1)/parts-1
		wg.Go(func() {
			err := writeRange(ctx, source, file, start, end, size, etag, progress)
			if err != nil {
				once.Do(func() {
					first = err
					cancel()
				})
			}
		})
	}
	wg.Wait()
	return first
}

func writeRange(ctx context.Context, source string, file *os.File, start, end, size int64, etag string, progress func(int64, int64)) error {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end)
	res, err := getHTTP(ctx, source, rangeHeader)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, size)
	if res.StatusCode != http.StatusPartialContent || res.Header.Get("Content-Range") != expected ||
		(etag != "" && res.Header.Get("ETag") != etag) {
		return fmt.Errorf("下载源未返回所请求的分段")
	}
	var out io.Writer = io.NewOffsetWriter(file, start)
	if progress != nil {
		out = progressWriter{out, size, progress}
	}
	_, err = io.CopyN(out, res.Body, end-start+1)
	return err
}

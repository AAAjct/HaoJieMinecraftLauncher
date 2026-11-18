package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Minute, Transport: func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 40
	transport.MaxConnsPerHost = 48
	transport.ResponseHeaderTimeout = 40 * time.Second
	return transport
}()}

type Engine struct {
	DataDir  string
	GameRoot string
}

func (e Engine) gameRoot() string {
	if e.GameRoot != "" {
		return e.GameRoot
	}
	return e.DataDir
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func get(ctx context.Context, url string) (*http.Response, error) {
	return getHTTP(ctx, url, "")
}

func getHTTP(ctx context.Context, url, byteRange string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HaoJieMinecraftLauncher/1.0")
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
		req.Header.Set("Accept-Encoding", "identity")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK && !(byteRange != "" && res.StatusCode == http.StatusPartialContent) {
		res.Body.Close()
		return nil, httpFailure{host: req.URL.Host, status: res.StatusCode}
	}
	return res, nil
}

type httpFailure struct {
	host   string
	status int
}

func (e httpFailure) Error() string { return fmt.Sprintf("%s 返回 HTTP %d", e.host, e.status) }

func fetchJSON(ctx context.Context, url string, result any) error {
	var last error
	for attempt := range 3 {
		if attempt != 0 {
			timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		res, err := get(ctx, url)
		if err == nil {
			err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(result)
			res.Body.Close()
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				return err
			}
		}
		if failure, ok := err.(httpFailure); ok && failure.status != http.StatusTooManyRequests && failure.status < 500 {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last = err
	}
	return last
}

func download(ctx context.Context, url, dir string, limit int64, label string, status func(string)) (string, error) {
	status("正在下载 " + label + "…")
	res, err := get(ctx, url)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	file, err := os.CreateTemp(dir, "hjml-download-*")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(file.Name())
		}
	}()

	progress := &downloadProgress{label: label, status: status, start: time.Now()}
	size, err := copyHTTP(ctx, url, res, file, nil, limit, progress.add)
	if err != nil {
		return "", err
	}
	if size > limit {
		return "", fmt.Errorf("%s 下载文件超过大小限制", label)
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return file.Name(), nil
}

type downloadProgress struct {
	mu       sync.Mutex
	start    time.Time
	received int64
	last     time.Time
	label    string
	status   func(string)
}

func (p *downloadProgress) add(n, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 0 {
		p.received = 0
		p.start = time.Now()
	} else {
		p.received += n
	}
	if time.Since(p.last) >= 250*time.Millisecond {
		rate := float64(p.received) / (1 << 20) / max(time.Since(p.start).Seconds(), 0.1)
		if total > 0 {
			p.status(fmt.Sprintf("正在下载 %s · %d%% · %.1f MiB/s", p.label, p.received*100/total, rate))
		} else {
			p.status(fmt.Sprintf("正在下载 %s · %.1f MB", p.label, float64(p.received)/(1<<20)))
		}
		p.last = time.Now()
	}
}

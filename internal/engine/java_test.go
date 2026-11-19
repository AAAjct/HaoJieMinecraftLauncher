package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLauncherJavaMajors(t *testing.T) {
	for _, test := range []struct{ required, want int }{
		{0, 8}, {8, 8}, {16, 17}, {17, 17}, {21, 21}, {25, 25},
	} {
		if got, err := launcherJavaMajor(test.required); err != nil || got != test.want {
			t.Errorf("Java %d: got %d, %v; want %d", test.required, got, err, test.want)
		}
	}
	e := Engine{JavaRoot: filepath.Join(t.TempDir(), "jdk")}
	if _, err := e.EnsureJava(t.Context(), 26, func(string) { t.Fatal("unexpected download") }); err == nil {
		t.Fatal("Java 26 should be rejected before querying Azul")
	}
	if _, err := os.Stat(e.JavaRoot); !os.IsNotExist(err) {
		t.Fatalf("unsupported runtime created a JDK directory: %v", err)
	}
}

func TestExtractJDK(t *testing.T) {
	for _, entry := range []string{"zulu/bin/java.exe", "../outside"} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "java.zip")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			archive := zip.NewWriter(file)
			writer, err := archive.Create(entry)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte("test")); err != nil {
				t.Fatal(err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "unzipped")
			err = extractJDK(t.Context(), path, target)
			if strings.HasPrefix(entry, "..") {
				if err == nil || regularFile(filepath.Join(dir, "outside")) {
					t.Fatalf("unsafe archive accepted: %v", err)
				}
			} else if err != nil || !regularFile(filepath.Join(target, entry)) {
				t.Fatalf("valid archive failed: %v", err)
			}
		})
	}
}

func TestConcurrentJavaInstallReusesJDK21(t *testing.T) {
	e := Engine{DataDir: t.TempDir()}
	archives := make(map[int][]byte)
	for _, major := range []int{17, 21} {
		var data bytes.Buffer
		z := zip.NewWriter(&data)
		for path, contents := range map[string]string{
			"zulu/bin/java.exe":  "test JVM",
			"zulu/bin/javac.exe": "test compiler",
			"zulu/release":       fmt.Sprintf("JAVA_VERSION=\"%d.0.1\"\n", major),
		} {
			entry, err := z.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(entry, contents); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		archives[major] = data.Bytes()
	}
	var mu sync.Mutex
	downloads := make(map[int]int)
	previous := client.Transport
	t.Cleanup(func() { client.Transport = previous })
	client.Transport = curseTestTransport(func(req *http.Request) (*http.Response, error) {
		for major, archive := range archives {
			id := fmt.Sprintf("00000000-0000-0000-0000-%012d", major)
			name := fmt.Sprintf("zulu%d-win_x64.zip", major)
			address := "https://cdn.azul.com/" + name
			var body []byte
			switch {
			case req.URL.Query().Get("java_version") == fmt.Sprint(major):
				body = fmt.Appendf(nil, `[{"package_uuid":%q,"name":%q,"java_version":[%d,0,1]}]`, id, name, major)
			case req.URL.String() == azulAPI+id:
				body = fmt.Appendf(nil, `{"download_url":%q,"sha256_hash":"%x"}`, address, sha256.Sum256(archive))
			case req.URL.String() == address:
				mu.Lock()
				downloads[major]++
				mu.Unlock()
				body = archive
			default:
				continue
			}
			return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
		}
		return nil, fmt.Errorf("unexpected fixture URL %s", req.URL)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		workers.Wait()
	})
	type result struct {
		home string
		err  error
	}
	start := func(major int, status func(string)) <-chan result {
		done := make(chan result, 1)
		workers.Go(func() {
			home, err := e.EnsureJava(ctx, major, status)
			done <- result{home, err}
		})
		return done
	}
	extracting, proceed, waiting := make(chan struct{}), make(chan struct{}), make(chan struct{})
	first := start(21, func(status string) {
		if strings.HasPrefix(status, "正在解压") {
			close(extracting)
			select {
			case <-proceed:
			case <-ctx.Done():
			}
		}
	})
	select {
	case <-extracting:
	case result := <-first:
		t.Fatalf("first JDK 21 task did not reach extraction: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := start(17, func(string) {})
	third := start(21, func(status string) {
		if strings.HasPrefix(status, "正在等待") {
			close(waiting)
		}
		if strings.HasPrefix(status, "正在解压") {
			t.Error("the second JDK 21 task extracted a separate copy")
		}
	})
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	check := func(done <-chan result, major int) {
		t.Helper()
		select {
		case result := <-done:
			if result.err != nil || result.home != e.javaHome(major) || !isJDK(result.home, major) {
				t.Fatalf("JDK %d result: %+v", major, result)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	check(second, 17)
	close(proceed)
	check(first, 21)
	check(third, 21)
	if entries, err := os.ReadDir(filepath.Join(e.gameRoot(), ".hjm-cache", "temp")); err != nil || len(entries) != 0 {
		t.Fatalf("JDK staging files remain: %v (%v)", entries, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if downloads[17] != 1 || downloads[21] != 1 {
		t.Fatalf("download counts = %v, want one per version", downloads)
	}
}

func TestWaitingJavaReusesJDKBeforeLockIsReleased(t *testing.T) {
	e := Engine{JavaRoot: t.TempDir()}
	lock, err := os.Create(filepath.Join(e.JavaRoot, ".zulu-21.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if claimed, err := waitFileLock(t.Context(), lock, nil); !claimed || err != nil {
		t.Fatalf("cannot hold first install lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		home, err := e.EnsureJava(ctx, 21, func(status string) {
			if strings.HasPrefix(status, "正在等待") {
				close(waiting)
			}
		})
		if err == nil && home != e.javaHome(21) {
			err = fmt.Errorf("unexpected JDK home %s", home)
		}
		done <- err
	}()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	staged := t.TempDir()
	if err := os.Mkdir(filepath.Join(staged, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		"bin/java.exe": "test JVM", "bin/javac.exe": "test compiler", "release": "JAVA_VERSION=\"21.0.13.1\"\n",
	} {
		if err := os.WriteFile(filepath.Join(staged, path), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(staged, e.javaHome(21)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("published JDK 21 remained blocked behind its install lock")
	}
}

func TestLiveJavaInstall(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to download Azul Zulu Java")
	}
	exeDir := t.TempDir()
	e := Engine{DataDir: t.TempDir(), JavaRoot: filepath.Join(exeDir, "jdk")}
	want := filepath.Join(exeDir, "jdk", "zulu 25")
	if err := os.MkdirAll(filepath.Join(want, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	home, err := e.EnsureJava(t.Context(), 25, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if home != want {
		t.Fatalf("Java installed to %q, want %q", home, want)
	}
	output, err := exec.CommandContext(t.Context(), filepath.Join(home, "bin", "java.exe"), "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(output), `version "25`) {
		t.Fatalf("downloaded JDK failed: %v\n%s", err, output)
	}
	if found, err := e.EnsureJava(t.Context(), 25, func(status string) {
		t.Fatalf("existing JDK should be reused immediately, got %q", status)
	}); found != home || err != nil {
		t.Fatalf("JDK reuse: %q %v", found, err)
	}
}

func TestNormalizeJDKDirectoryCase(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "Jdk", "Zulu 21"), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "jdk")
	for _, name := range []string{root, filepath.Join(root, "zulu 21")} {
		if err := normalizeDirectoryCase(t.Context(), name); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Dir(name))
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(name) {
			t.Fatalf("directory name did not change to %s: %v (%v)", name, entries, err)
		}
	}
}

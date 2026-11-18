package engine

import (
	"archive/zip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

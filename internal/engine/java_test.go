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
	e := Engine{DataDir: t.TempDir()}
	home, err := e.EnsureJava(t.Context(), 21, func(message string) { t.Log(message) })
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), filepath.Join(home, "bin", "java.exe"), "-version").CombinedOutput()
	if err != nil || !strings.Contains(string(output), `version "21.`) {
		t.Fatalf("downloaded JDK failed: %v\n%s", err, output)
	}
}

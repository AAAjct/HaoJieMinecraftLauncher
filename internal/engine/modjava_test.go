package engine

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFabricJavaMinimumWithForgeConnector(t *testing.T) {
	game := t.TempDir()
	mods := filepath.Join(game, "mods")
	if err := os.Mkdir(mods, 0700); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ filename, path, text string }{
		{"Zoomify.jar", "fabric.mod.json", `{"id":"zoomify","name":"Zoomify","depends":{"java":">=21"}}`},
		{"server.jar", "fabric.mod.json", `{"id":"server","environment":"server","depends":{"java":">=25"}}`},
		{"disabled.jar.disabled", "fabric.mod.json", `{"id":"disabled","depends":{"java":">=25"}}`},
		{"Connector.jar.disabled", "META-INF/MANIFEST.MF", "Automatic-Module-Name: org.sinytra.connector\r\n"},
	} {
		file, err := os.Create(filepath.Join(mods, fixture.filename))
		if err != nil {
			t.Fatal(err)
		}
		z := zip.NewWriter(file)
		entry, err := z.Create(fixture.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, fixture.text); err != nil {
			t.Fatal(err)
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, err := fabricJavaMinimum(t.Context(), game, "neoforge"); err != nil || got != 0 {
		t.Fatalf("without Connector: %d, %v", got, err)
	}
	if err := os.Rename(filepath.Join(mods, "Connector.jar.disabled"), filepath.Join(mods, "Connector.jar")); err != nil {
		t.Fatal(err)
	}
	if got, mod, err := fabricJavaMinimum(t.Context(), game, "neoforge"); err != nil || got != 21 || mod != "Zoomify" {
		t.Fatalf("with Connector: %d, %s, %v", got, mod, err)
	}
}

package engine

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractWorld(t *testing.T) {
	for _, args := range []struct {
		name  string
		files []string
		world string
	}{
		{"flat", []string{"level.dat", "region/r.0.0.mca"}, "."},
		{"wrapped", []string{"readme.txt", "bundle/World/level.dat", "bundle/World/region/r.0.0.mca"}, "World"},
		{"traversal", []string{"World/level.dat", "World/../outside.dat"}, ""},
		{"multiple", []string{"One/level.dat", "Two/level.dat"}, ""},
	} {
		t.Run(args.name, func(t *testing.T) {
			temp := t.TempDir()
			input := filepath.Join(temp, "source.zip")
			file, err := os.Create(input)
			if err != nil {
				t.Fatal(err)
			}
			archive := zip.NewWriter(file)
			for _, name := range args.files {
				entry, err := archive.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("world fixture")); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(temp, "extracted")
			world, err := extractWorld(t.Context(), input, dest)
			if args.world == "" {
				if err == nil || regularFile(filepath.Join(temp, "outside.dat")) {
					t.Fatalf("invalid archive accepted: %q, %v", world, err)
				}
				return
			}
			if err != nil || world != args.world || !regularFile(filepath.Join(dest, "level.dat")) ||
				!regularFile(filepath.Join(dest, "region", "r.0.0.mca")) || regularFile(filepath.Join(dest, "readme.txt")) {
				t.Fatalf("world contents or name were not preserved: %q, %v", world, err)
			}
		})
	}
}

package engine

import (
	"io/fs"
	"os"
	"path/filepath"
)

func (e Engine) Worlds(instance Instance) ([]string, error) {
	root, err := os.OpenRoot(e.GameDir(instance))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), "saves")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var worlds []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := root.Stat(filepath.Join("saves", entry.Name(), "level.dat"))
		if err == nil && info.Mode().IsRegular() {
			worlds = append(worlds, entry.Name())
		}
	}
	return worlds, nil
}

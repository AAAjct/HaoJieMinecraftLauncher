package engine

import (
	"os"
	"path/filepath"
)

type Settings struct {
	Nickname     string
	Instance     string
	Skin         string `json:",omitempty"`
	SkinPNG      []byte `json:",omitempty"`
	SkinFilename string `json:",omitempty"`
}

func (e Engine) LoadSettings() Settings {
	s := Settings{Nickname: "Player"}
	_ = readJSON(filepath.Join(e.DataDir, "settings.json"), &s)
	return s
}

func (e Engine) SaveSettings(s Settings) error {
	if err := os.MkdirAll(e.DataDir, 0700); err != nil {
		return err
	}
	return writeJSON(filepath.Join(e.DataDir, "settings.json"), s)
}

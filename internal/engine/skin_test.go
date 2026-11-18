package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveAlexPreviewFetchAndCache(t *testing.T) {
	if os.Getenv("HJML_INTEGRATION") != "1" {
		t.Skip("set HJML_INTEGRATION=1 to download the Alex preview")
	}
	e := Engine{DataDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	data, err := e.FetchAlexPreview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlayerSkin(data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, e.CachedAlexPreview()) {
		t.Fatal("downloaded Alex preview wasn't cached")
	}
}

func TestPlayerSkinPackUsesOfflineTextureAndPreservesOtherPacks(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "client.jar")
	file, err := os.Create(client)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	skin := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	skin.SetNRGBA(8, 8, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&data, skin); err != nil {
		t.Fatal(err)
	}
	jar := zip.NewWriter(file)
	for name, contents := range map[string][]byte{
		"version.json":                          []byte(`{"pack_version":{"resource_major":97,"resource_minor":1}}`),
		playerTextures + "player/slim/alex.png": data.Bytes(),
	} {
		entry, err := jar.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := jar.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	optionsPath := filepath.Join(dir, "options.txt")
	if err := os.WriteFile(optionsPath, []byte("lang:zh_cn\nresourcePacks:[\"file/Existing.zip\",\"file/hjml-player-skin-old.zip\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	account := Account{UUID: strings.Repeat("0", 32)}
	if err := applyPlayerSkin(client, dir, account, LaunchOptions{Skin: LocalSkin, SkinPNG: data.Bytes()}); err != nil {
		t.Fatal(err)
	}
	packs, err := os.ReadDir(filepath.Join(dir, "resourcepacks"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected one generated resource pack, got %v: %v", packs, err)
	}
	options, err := os.ReadFile(optionsPath)
	if err != nil || string(options) != "lang:zh_cn\nresourcePacks:[\"file/Existing.zip\",\"file/"+packs[0].Name()+"\"]\n" {
		t.Fatalf("unexpected options: %s (%v)", options, err)
	}
	pack, err := zip.OpenReader(filepath.Join(dir, "resourcepacks", packs[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer pack.Close()
	if meta := string(modEntry(&pack.Reader, "pack.mcmeta")); !strings.Contains(meta, `"max_format":[97,1]`) {
		t.Fatalf("resource pack version does not match the game: %s", meta)
	}
	texture := playerTextures + "player/slim/alex.png"
	decoded, err := DecodePlayerSkin(modEntry(&pack.Reader, texture))
	if err != nil || decoded.Bounds().Dy() != 64 || color.NRGBAModel.Convert(decoded.At(8, 8)) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("legacy skin wasn't expanded into the correct default skin slot: %v", err)
	}
}

func TestAdditionalGameSessionSharesSavesAndIsolatesOptions(t *testing.T) {
	dir := t.TempDir()
	options := filepath.Join(dir, "options.txt")
	if err := os.WriteFile(options, []byte("fullscreen:false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := startGameSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err := os.WriteFile(filepath.Join(session.dir, "options.txt"), []byte("fullscreen:true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.dir, "saves", "fixture"), []byte("world"), 0600); err != nil {
		t.Fatal(err)
	}
	session.close()
	if data, err := os.ReadFile(options); err != nil || string(data) != "fullscreen:false\n" {
		t.Fatalf("session overwrote the instance's options: %s %v", data, err)
	}
	if !regularFile(filepath.Join(dir, "saves", "fixture")) {
		t.Fatal("the shared save didn't survive the session")
	}
	if _, err := os.Stat(session.dir); !os.IsNotExist(err) {
		t.Fatalf("session directory still exists: %v", err)
	}
}

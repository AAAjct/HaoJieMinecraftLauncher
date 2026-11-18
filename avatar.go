package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

func (a *app) playerAvatar(c *ui.Context) {
	kind := a.settings.Skin
	if kind == "" {
		kind = engine.SteveSkin
	}
	avatar, label := a.skinAvatar, "本地皮肤头像"
	switch kind {
	case engine.SteveSkin:
		avatar, label = steveAvatar, "史蒂夫头像"
	case engine.AlexSkin:
		label = "艾利克斯头像"
	}
	ui.Column(c).Size(64, 64).Center().Label(label).
		ContextMenu(func(menu *ui.Menu) {
			next := a.settings
			if menu.Item("史蒂夫").Checked(a.settings.Skin == engine.SteveSkin).Chosen() {
				next.Skin = engine.SteveSkin
				a.saveSkin(next)
			}
			if menu.Item("艾利克斯").Checked(kind == engine.AlexSkin).Chosen() {
				next.Skin = engine.AlexSkin
				a.saveSkin(next)
			}
			localLabel := "本地皮肤"
			name := strings.TrimSuffix(next.SkinFilename, filepath.Ext(next.SkinFilename))
			if len(next.SkinPNG) > 0 && name != "" {
				localLabel = name
			}
			if menu.Item(strings.ReplaceAll(localLabel, "&", "&&")).Checked(kind == engine.LocalSkin).Chosen() {
				if len(a.settings.SkinPNG) == 0 {
					a.chooseSkin()
				} else {
					next.Skin = engine.LocalSkin
					a.saveSkin(next)
				}
			}
			if len(a.settings.SkinPNG) > 0 && menu.Item("导入本地皮肤…").Chosen() {
				a.chooseSkin()
			}
		}).Children(func() {
		if avatar != nil {
			ui.Image(c, avatar).Size(64, 64)
		}
	})
}

func (a *app) refreshSkinAvatar() {
	a.skinAvatar = nil
	var data []byte
	switch a.settings.Skin {
	case engine.AlexSkin:
		if selected := a.current(); selected.ID != "" {
			data, _ = a.engine.DefaultSkin(selected, engine.AlexSkin)
		}
		for _, instance := range a.instances {
			if len(data) != 0 {
				break
			}
			data, _ = a.engine.DefaultSkin(instance, engine.AlexSkin)
		}
		if len(data) == 0 {
			data = a.engine.CachedAlexPreview()
		}
		if len(data) == 0 {
			a.fetchAlexAvatar()
		}
	case engine.LocalSkin:
		data = a.settings.SkinPNG
	}
	if len(data) != 0 {
		a.skinAvatar, _ = skinFace(data)
	}
}

func (a *app) fetchAlexAvatar() {
	if a.window == nil || a.alexLoading {
		return
	}
	a.alexLoading = true
	go func() {
		ctx, cancel := context.WithTimeout(a.catalogContext, 8*time.Second)
		defer cancel()
		data, err := a.engine.FetchAlexPreview(ctx)
		if a.catalogContext.Err() != nil {
			return
		}
		a.window.Update(func() {
			a.alexLoading = false
			if a.settings.Skin == engine.AlexSkin && a.skinAvatar == nil {
				if err != nil {
					a.status = "无法加载艾利克斯头像，请联网后重新选择。"
					return
				}
				a.skinAvatar, _ = skinFace(data)
			}
		})
	}()
}

func skinFace(data []byte) (*ui.Bitmap, error) {
	source, err := engine.DecodePlayerSkin(data)
	if err != nil {
		return nil, err
	}
	face := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(face, face.Bounds(), source, image.Pt(8, 8), draw.Src)
	draw.Draw(face, face.Bounds(), source, image.Pt(40, 8), draw.Over)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, face); err != nil {
		return nil, err
	}
	bitmap, err := ui.DecodeBitmap(encoded.Bytes())
	if err != nil {
		return nil, err
	}
	return bitmap.Pixelated(), nil
}

func (a *app) saveSkin(next engine.Settings) {
	if err := a.engine.SaveSettings(next); err != nil {
		a.status = err.Error()
		return
	}
	a.settings = next
	a.refreshSkinAvatar()
	a.status = "已切换皮肤，下次启动游戏时应用。"
}

func (a *app) chooseSkin() {
	if a.window == nil {
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.window, Title: "导入 Minecraft 皮肤",
			Filters: []mygo.FileFilter{{Name: "PNG 皮肤", Extensions: []string{"png"}}},
		})
		var data []byte
		if err == nil && len(paths) > 0 {
			data, err = loadSkinFile(paths[0])
		}
		a.window.Update(func() {
			if err != nil {
				a.status = err.Error()
			} else if len(data) > 0 {
				next := a.settings
				next.Skin, next.SkinPNG = engine.LocalSkin, data
				next.SkinFilename = filepath.Base(paths[0])
				a.saveSkin(next)
			}
		})
	}()
}

func loadSkinFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 1 << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("PNG 皮肤文件超过 1 MiB")
	}
	if _, err := engine.DecodePlayerSkin(data); err != nil {
		return nil, err
	}
	return data, nil
}

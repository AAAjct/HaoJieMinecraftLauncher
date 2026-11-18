package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const alexPreviewURL = "https://assets.mcasset.cloud/26.3/assets/minecraft/textures/entity/player/slim/alex.png"
const alexPreviewHash = "99391ed9dff9581ec88daf23b9b028adc9ac91f52889edcff1c982d0923c0cff"

func (e Engine) CachedAlexPreview() []byte {
	data, err := os.ReadFile(filepath.Join(e.DataDir, "alex-preview.png"))
	if err != nil || !validAlexPreview(data) {
		return nil
	}
	return data
}

func (e Engine) FetchAlexPreview(ctx context.Context) ([]byte, error) {
	res, err := get(ctx, alexPreviewURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil || !validAlexPreview(data) {
		return nil, fmt.Errorf("艾利克斯预览头像校验失败")
	}
	if os.MkdirAll(e.DataDir, 0700) == nil {
		_ = writeFile(filepath.Join(e.DataDir, "alex-preview.png"), data)
	}
	return data, nil
}

func validAlexPreview(data []byte) bool {
	return fmt.Sprintf("%x", sha256.Sum256(data)) == alexPreviewHash
}

const (
	SteveSkin      = "steve"
	AlexSkin       = "alex"
	LocalSkin      = "local"
	skinPackPrefix = "hjml-player-skin-"
	playerTextures = "assets/minecraft/textures/entity/"
)

func DecodePlayerSkin(data []byte) (image.Image, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 64 || (config.Height != 32 && config.Height != 64) {
		return nil, fmt.Errorf("请选择 64×64 或 64×32 的 Minecraft PNG 皮肤")
	}
	return png.Decode(bytes.NewReader(data))
}

func (e Engine) DefaultSkin(instance Instance, kind string) ([]byte, error) {
	if instance.legacy {
		e.GameRoot = ""
	}
	z, err := zip.OpenReader(e.baseVersionFile(instance.Version, ".jar"))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	return defaultSkinPNG(&z.Reader, kind, kind == AlexSkin)
}

func defaultSkinPNG(z *zip.Reader, kind string, slim bool) ([]byte, error) {
	model := "wide"
	if slim {
		model = "slim"
	}
	data := modEntry(z, playerTextures+"player/"+model+"/"+kind+".png")
	if len(data) == 0 {
		data = modEntry(z, playerTextures+kind+".png")
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("该 Minecraft 版本缺少所选预设皮肤")
	}
	return data, nil
}

func skinTexturePath(z *zip.Reader, uuid string) (path string, slim, old bool, err error) {
	id, err := hex.DecodeString(uuid)
	if err != nil || len(id) != 16 {
		return "", false, false, fmt.Errorf("无效的离线玩家 UUID")
	}
	hash := int32(binary.BigEndian.Uint32(id[:4]) ^ binary.BigEndian.Uint32(id[4:8]) ^
		binary.BigEndian.Uint32(id[8:12]) ^ binary.BigEndian.Uint32(id[12:]))
	if _, err := fs.Stat(z, playerTextures+"player/slim/alex.png"); err == nil {
		names := []string{"alex", "ari", "efe", "kai", "makena", "noor", "steve", "sunny", "zuri"}
		index := int((hash%18 + 18) % 18)
		model := "wide"
		if index < len(names) {
			model = "slim"
		}
		return playerTextures + "player/" + model + "/" + names[index%len(names)] + ".png", model == "slim", false, nil
	}
	if _, err := fs.Stat(z, playerTextures+"alex.png"); err == nil && hash&1 != 0 {
		return playerTextures + "alex.png", true, true, nil
	}
	return playerTextures + "steve.png", false, true, nil
}

func applyPlayerSkin(client, game string, account Account, options LaunchOptions) error {
	z, err := zip.OpenReader(client)
	if err != nil {
		return err
	}
	defer z.Close()
	texture, slim, old, err := skinTexturePath(&z.Reader, account.UUID)
	if err != nil {
		return err
	}
	data := options.SkinPNG
	if options.Skin != LocalSkin {
		data, err = defaultSkinPNG(&z.Reader, options.Skin, slim)
		if err != nil {
			return err
		}
	}
	skin, err := DecodePlayerSkin(data)
	if err != nil {
		return err
	}
	if skin.Bounds().Dy() == 32 {
		var pngData bytes.Buffer
		if err := png.Encode(&pngData, expandLegacySkin(skin)); err != nil {
			return err
		}
		data = pngData.Bytes()
	}
	format, metadata := skinPackMetadata(&z.Reader)
	var contents bytes.Buffer
	pack := zip.NewWriter(&contents)
	for name, file := range map[string][]byte{"pack.mcmeta": metadata, texture: data} {
		entry, err := pack.Create(name)
		if err != nil {
			return err
		}
		if _, err := entry.Write(file); err != nil {
			return err
		}
	}
	if err := pack.Close(); err != nil {
		return err
	}
	key := sha256.Sum256(append([]byte(texture), data...))
	filename := skinPackPrefix + hex.EncodeToString(key[:8]) + ".zip"
	folder := filepath.Join(game, "resourcepacks")
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	path := filepath.Join(folder, filename)
	if !regularFile(path) {
		if err := writeFile(path, contents.Bytes()); err != nil {
			return err
		}
	}
	if !old || format >= 4 {
		filename = "file/" + filename
	}
	return enableSkinPack(game, filename)
}

func skinPackMetadata(z *zip.Reader) (int, []byte) {
	var version struct {
		Pack struct {
			Resource int
			Major    int `json:"resource_major"`
			Minor    int `json:"resource_minor"`
		} `json:"pack_version"`
	}
	_ = json.Unmarshal(modEntry(z, "version.json"), &version)
	format := max(version.Pack.Major, version.Pack.Resource)
	if format == 0 {
		format = 1
		_, modernAssets := fs.Stat(z, playerTextures+"player/slim/alex.png")
		if modernAssets == nil {
			format = 12
		}
	}
	var metadata string
	if version.Pack.Major > 0 {
		metadata = fmt.Sprintf(`{"pack":{"description":"HJML 皮肤","min_format":[%d,%d],"max_format":[%d,%d]}}`,
			format, version.Pack.Minor, format, version.Pack.Minor)
	} else {
		metadata = fmt.Sprintf(`{"pack":{"description":"HJML 皮肤","pack_format":%d}}`, format)
	}
	return format, []byte(metadata)
}

func enableSkinPack(game, filename string) error {
	path := filepath.Join(game, "options.txt")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(data)
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.TrimRight(text, "\r\n"), newline)
	var packs []string
	result := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if value, ok := strings.CutPrefix(line, "resourcePacks:"); ok {
			if err := json.Unmarshal([]byte(value), &packs); err != nil {
				return fmt.Errorf("无法读取游戏现有的资源包设置：%w", err)
			}
		} else if line != "" {
			result = append(result, line)
		}
	}
	packs = slices.DeleteFunc(packs, func(name string) bool {
		return strings.HasPrefix(strings.TrimPrefix(name, "file/"), skinPackPrefix)
	})
	encoded, err := json.Marshal(append(packs, filename))
	if err != nil {
		return err
	}
	result = append(result, "resourcePacks:"+string(encoded))
	if next := strings.Join(result, newline) + newline; next != text {
		return writeFile(path, []byte(next))
	}
	return nil
}

func expandLegacySkin(skin image.Image) image.Image {
	out := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	draw.Draw(out, out.Bounds(), skin, image.Point{}, draw.Src)
	for _, limb := range []struct{ source, target image.Point }{
		{image.Pt(0, 16), image.Pt(16, 48)},
		{image.Pt(40, 16), image.Pt(32, 48)},
	} {
		for _, face := range []struct{ from, to, y, width, height int }{
			{4, 4, 0, 4, 4}, {8, 8, 0, 4, 4},
			{0, 8, 4, 4, 12}, {4, 4, 4, 4, 12}, {8, 0, 4, 4, 12}, {12, 12, 4, 4, 12},
		} {
			for y := range face.height {
				for x := range face.width {
					out.Set(limb.target.X+face.to+x, limb.target.Y+face.y+y,
						skin.At(limb.source.X+face.from+face.width-1-x, limb.source.Y+face.y+y))
				}
			}
		}
	}
	return out
}

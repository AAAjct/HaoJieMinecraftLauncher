package engine

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	_ "image/png"
	"io"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type modMenu struct {
	Badges []string
}

type modDisplay struct {
	Name, Description string
	Icon              json.RawMessage
}

func readModInfo(file, loader string, mod *Mod) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return
	}
	defer z.Close()

	var icon string
	manifest := "META-INF/mods.toml"
	if loader == "neoforge" {
		manifest = "META-INF/neoforge.mods.toml"
	}
	if data := modEntry(&z.Reader, manifest); len(data) != 0 {
		var meta struct {
			Mods []struct {
				ModID, DisplayName, Version, Description, LogoFile string
			}
		}
		if toml.Unmarshal(data, &meta) == nil && len(meta.Mods) > 0 {
			entry := meta.Mods[0]
			mod.ID, mod.Name, mod.Version, mod.Description = entry.ModID, entry.DisplayName, entry.Version, entry.Description
			icon = entry.LogoFile
			if mod.Version == "${file.jarVersion}" {
				mod.Version = ""
				for _, line := range strings.Split(string(modEntry(&z.Reader, "META-INF/MANIFEST.MF")), "\n") {
					if version, found := strings.CutPrefix(line, "Implementation-Version:"); found {
						mod.Version = strings.TrimSpace(version)
						break
					}
				}
			}
		}
	}
	if mod.ID == "" && loader == "quilt" {
		var meta struct {
			QuiltLoader struct {
				ID, Version string
				Metadata    struct {
					modDisplay
					ModMenu modMenu
				}
			} `json:"quilt_loader"`
			Minecraft struct{ Environment string }
		}
		if json.Unmarshal(modEntry(&z.Reader, "quilt.mod.json"), &meta) == nil {
			entry := meta.QuiltLoader
			mod.ID, mod.Name, mod.Version, mod.Description = entry.ID, entry.Metadata.Name, entry.Version, entry.Metadata.Description
			mod.Tags = localModTags(entry.Metadata.ModMenu.Badges, meta.Minecraft.Environment)
			icon = modIconPath(entry.Metadata.Icon)
		}
	}
	if mod.ID == "" {
		var meta struct {
			modDisplay
			ID, Version, Environment string
			Custom                   struct{ ModMenu modMenu }
		}
		if json.Unmarshal(modEntry(&z.Reader, "fabric.mod.json"), &meta) == nil {
			mod.ID, mod.Name, mod.Version, mod.Description = meta.ID, meta.Name, meta.Version, meta.Description
			mod.Tags = localModTags(meta.Custom.ModMenu.Badges, meta.Environment)
			icon = modIconPath(meta.Icon)
		}
	}
	if mod.Version == "" && mod.ID != "" {
		base := strings.TrimSuffix(strings.TrimSuffix(mod.Filename, ".disabled"), ".jar")
		if version, ok := strings.CutPrefix(base, mod.ID+"-"); ok {
			mod.Version = version
		}
	}
	if icon != "" {
		data := modEntry(&z.Reader, icon)
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil && config.Width <= 1024 && config.Height <= 1024 {
			mod.Icon = data
		}
	}
}

func modEntry(z *zip.Reader, name string) []byte {
	file, err := z.Open(name)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil
	}
	return data
}

func modIconPath(icon json.RawMessage) string {
	var name string
	if json.Unmarshal(icon, &name) == nil {
		return name
	}
	var sizes map[string]string
	if json.Unmarshal(icon, &sizes) == nil {
		best := 0
		for key, file := range sizes {
			size, err := strconv.Atoi(key)
			if err == nil && size > 0 && (best == 0 || best < 64 && size > best || size >= 64 && size < best) {
				best, name = size, file
			}
		}
	}
	return name
}

func localModTags(badges []string, environment string) []string {
	var tags []string
	for _, badge := range badges {
		switch badge {
		case "library":
			tags = append(tags, "支持库")
		case "deprecated":
			tags = append(tags, "已弃用")
		default:
			tags = append(tags, badge)
		}
	}
	switch environment {
	case "client":
		tags = append(tags, "客户端")
	case "server", "dedicated_server":
		tags = append(tags, "服务端")
	}
	return tags
}

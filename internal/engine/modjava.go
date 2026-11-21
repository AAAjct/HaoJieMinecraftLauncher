package engine

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func fabricJavaMinimum(ctx context.Context, gameDir, loader string) (int, string, error) {
	forgeFamily := forgeFamilyName(loader) != ""
	if loader != "fabric" && !forgeFamily {
		return 0, "", nil
	}
	dir := filepath.Join(gameDir, "mods")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	var minimum int
	var modName string
	connector := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".jar") {
			continue
		}
		archive, err := zip.OpenReader(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if forgeFamily && !connector {
			connector = hasConnectorManifest(&archive.Reader)
		}
		var mod struct {
			ID, Name, Environment string
			Depends               map[string]json.RawMessage
		}
		err = json.Unmarshal(modEntry(&archive.Reader, "fabric.mod.json"), &mod)
		archive.Close()
		if err != nil || mod.ID == "" || mod.Environment == "server" {
			continue
		}
		var predicate string
		if json.Unmarshal(mod.Depends["java"], &predicate) != nil {
			continue
		}
		bound, ok := strings.CutPrefix(strings.TrimSpace(predicate), ">=")
		major, err := strconv.Atoi(strings.TrimSpace(bound))
		if ok && err == nil && major > minimum {
			minimum = major
			modName = mod.Name
			if modName == "" {
				modName = mod.ID
			}
		}
	}
	if forgeFamily && !connector {
		return 0, "", nil
	}
	return minimum, modName, nil
}

func hasConnectorManifest(archive *zip.Reader) bool {
	for _, line := range strings.Split(string(modEntry(archive, "META-INF/MANIFEST.MF")), "\n") {
		if strings.TrimSpace(line) == "Automatic-Module-Name: org.sinytra.connector" {
			return true
		}
	}
	for _, name := range []string{"META-INF/mods.toml", "META-INF/neoforge.mods.toml"} {
		data := modEntry(archive, name)
		if len(data) == 0 {
			continue
		}
		var manifest struct{ Mods []struct{ ModID string } }
		if toml.Unmarshal(data, &manifest) == nil {
			for _, mod := range manifest.Mods {
				if mod.ModID == "connector" {
					return true
				}
			}
		}
	}
	return false
}

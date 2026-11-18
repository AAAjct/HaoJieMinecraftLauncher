package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Instance struct {
	ID      string
	Name    string
	Version string
	Loader  string
	Build   string
	Java    int
	Created time.Time
	legacy  bool
}

type Mod struct {
	Filename string
	Enabled  bool
	ID, Name string
}

func (e Engine) Instances() ([]Instance, error) {
	legacy, err := readInstances(filepath.Join(e.DataDir, "instances"), true)
	if err != nil || e.GameRoot == "" {
		return legacy, err
	}
	current, err := readInstances(filepath.Join(e.GameRoot, "versions"), false)
	instances := append(current, legacy...)
	slices.SortFunc(instances, func(a, b Instance) int { return b.Created.Compare(a.Created) })
	return instances, err
}

func readInstances(root string, legacy bool) ([]Instance, error) {
	list, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var instances []Instance
	for _, entry := range list {
		var instance Instance
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") &&
			readJSON(filepath.Join(root, entry.Name(), "instance.json"), &instance) == nil && instance.ID == entry.Name() {
			instance.legacy = legacy
			instances = append(instances, instance)
		}
	}
	slices.SortFunc(instances, func(a, b Instance) int { return b.Created.Compare(a.Created) })
	return instances, nil
}

func (e Engine) GameDir(instance Instance) string {
	if e.GameRoot != "" && !instance.legacy {
		return filepath.Join(e.GameRoot, "versions", instance.ID)
	}
	return filepath.Join(e.DataDir, "instances", instance.ID, "game")
}

func (e Engine) RenameInstance(instance Instance, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\r\n\x00") {
		return fmt.Errorf("请输入有效的实例名称")
	}
	if !segment(instance.ID) {
		return fmt.Errorf("无效的实例")
	}
	dir := e.GameDir(instance)
	if instance.legacy || e.GameRoot == "" {
		dir = filepath.Dir(dir)
	}
	path := filepath.Join(dir, "instance.json")
	var saved Instance
	if err := readJSON(path, &saved); err != nil {
		return err
	}
	if saved.ID != instance.ID {
		return fmt.Errorf("实例信息已更改，请刷新后重试")
	}
	saved.Name = name
	return writeJSON(path, saved)
}

func (e Engine) Install(ctx context.Context, instance Instance, status func(string)) (Instance, error) {
	return e.createInstance(ctx, instance, nil, status)
}

func (e Engine) createInstance(ctx context.Context, instance Instance, setup func(string) error, status func(string)) (Instance, error) {
	if strings.TrimSpace(instance.Name) == "" {
		instance.Name = instance.Version
		if instance.Loader != "" {
			instance.Name += " · " + instance.Loader
		}
	}
	if instance.Loader != "" && instance.Build == "" {
		build, err := nextLoader(ctx, instance.Version, instance.Loader)
		if err != nil {
			return Instance{}, err
		}
		instance.Build = build
	}
	root := filepath.Join(e.DataDir, "instances")
	if e.GameRoot != "" {
		root = filepath.Join(e.GameRoot, "versions")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Instance{}, err
	}
	stage, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return Instance{}, err
	}
	defer os.RemoveAll(stage)
	game := filepath.Join(stage, "game")
	if e.GameRoot != "" {
		game = stage
	}
	if err := os.MkdirAll(game, 0700); err != nil {
		return Instance{}, err
	}
	profile, err := e.installGame(ctx, instance, status)
	if err != nil {
		return Instance{}, err
	}
	instance.Java = profile.JavaVersion.MajorVersion
	if _, err := e.EnsureJava(ctx, instance.Java, status); err != nil {
		return Instance{}, err
	}
	if setup != nil {
		if err := setup(game); err != nil {
			return Instance{}, err
		}
	}
	indexPath := filepath.Join(e.gameRoot(), "assets", "indexes", profile.AssetIndex.ID+".json")
	if err := defaultChinese(game, indexPath); err != nil {
		return Instance{}, err
	}
	if instance.Loader == "fabric" {
		if err := installFabricAPI(ctx, instance, game, status); err != nil {
			return Instance{}, err
		}
	}
	var id [12]byte
	rand.Read(id[:])
	idSuffix := hex.EncodeToString(id[:])
	instance.ID = idSuffix
	if e.GameRoot != "" {
		instance.ID = instance.Version + "-Vanilla"
		if instance.Loader != "" {
			instance.ID = instance.Version + "-" + instance.Loader + "_" + instance.Build
		}
		if _, err := os.Stat(filepath.Join(root, instance.ID)); !os.IsNotExist(err) {
			instance.ID += "-" + idSuffix[:8]
		}
		if err := os.Link(e.baseVersionFile(instance.Version, ".jar"), filepath.Join(stage, instance.ID+".jar")); err != nil {
			return Instance{}, err
		}
		profile.ID = instance.ID
		if err := writeJSON(filepath.Join(stage, instance.ID+".json"), profile); err != nil {
			return Instance{}, err
		}
	}
	instance.Created = time.Now()
	if err := writeJSON(filepath.Join(stage, "instance.json"), instance); err != nil {
		return Instance{}, err
	}
	if err := os.Rename(stage, filepath.Join(root, instance.ID)); err != nil {
		return Instance{}, err
	}
	return instance, nil
}

func defaultChinese(game, indexPath string) error {
	var index assetIndex
	if err := readJSON(indexPath, &index); err != nil {
		return err
	}
	locale := "zh_cn"
	for name := range index.Objects {
		if filepath.Base(name) == "zh_CN.lang" {
			locale = "zh_CN"
			break
		}
	}
	path := filepath.Join(game, "options.txt")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	newline := "\n"
	if strings.Contains(string(data), "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(string(data), newline)
	lines = slices.DeleteFunc(lines, func(line string) bool { return strings.HasPrefix(line, "lang:") })
	options := strings.TrimRight(strings.Join(lines, newline), "\r\n")
	if options != "" {
		options += newline
	}
	return writeFile(path, []byte(options+"lang:"+locale+newline))
}

func (e Engine) Mods(instance Instance) ([]Mod, error) {
	entries, err := os.ReadDir(filepath.Join(e.GameDir(instance), "mods"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []Mod
	for _, entry := range entries {
		if entry.Type().IsRegular() && (strings.HasSuffix(strings.ToLower(entry.Name()), ".jar") ||
			strings.HasSuffix(strings.ToLower(entry.Name()), ".jar.disabled")) {
			mod := Mod{Filename: entry.Name(), Enabled: !strings.HasSuffix(strings.ToLower(entry.Name()), ".disabled")}
			var meta struct{ ID, Name string }
			if readModJSON(filepath.Join(e.GameDir(instance), "mods", entry.Name()), &meta) == nil {
				mod.ID, mod.Name = meta.ID, meta.Name
			}
			result = append(result, mod)
		}
	}
	return result, nil
}

func (e Engine) modPath(instance Instance, name string) (string, error) {
	if !segment(name) {
		return "", fmt.Errorf("无效模组文件名")
	}
	return filepath.Join(e.GameDir(instance), "mods", name), nil
}

func (e Engine) ToggleMod(instance Instance, mod Mod) error {
	file, err := e.modPath(instance, mod.Filename)
	if err != nil {
		return err
	}
	target := file + ".disabled"
	if !mod.Enabled {
		target = file[:len(file)-len(".disabled")]
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return fmt.Errorf("目标模组文件已存在或无法访问")
	}
	return os.Rename(file, target)
}

func (e Engine) RemoveMod(instance Instance, mod Mod) error {
	path, err := e.modPath(instance, mod.Filename)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

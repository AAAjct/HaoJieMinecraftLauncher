package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var instancePublishMu sync.Mutex
var versionLocks sync.Map

func (e Engine) versionLock(instance Instance) *sync.RWMutex {
	if instance.legacy {
		e.GameRoot = ""
	}
	key := filepath.Join(e.gameRoot(), "versions", instance.Version)
	lock, _ := versionLocks.LoadOrStore(key, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

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

func (i Instance) DefaultName() string {
	if i.Loader == "" {
		return i.Version
	}
	if i.Build == "" {
		return i.Version + "-" + i.Loader
	}
	return i.Version + "-" + i.Loader + "_" + i.Build
}

type Mod struct {
	Filename          string
	Enabled           bool
	ID, Name, Version string
	Description       string
	Tags              []string
	Icon              []byte
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
	instancePublishMu.Lock()
	defer instancePublishMu.Unlock()
	dir, saved, err := e.instanceMetadata(instance)
	if err != nil {
		return err
	}
	saved.Name, err = e.availableInstanceName(name, e.GameDir(instance))
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "instance.json"), saved)
}

// The caller holds instancePublishMu until it saves the allocated name.
func (e Engine) availableInstanceName(name, currentDir string) (string, error) {
	instances, err := e.Instances()
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(instances))
	for _, instance := range instances {
		if e.GameDir(instance) != currentDir {
			used[strings.ToLower(strings.TrimSpace(instance.Name))] = true
		}
	}
	candidate := name
	for n := 1; used[strings.ToLower(candidate)]; n++ {
		candidate = name + "(" + strconv.Itoa(n) + ")"
	}
	return candidate, nil
}

func (e Engine) RemoveInstance(instance Instance) error {
	dir, saved, err := e.instanceMetadata(instance)
	if err != nil {
		return err
	}
	saved.legacy = instance.legacy
	lock := e.versionLock(saved)
	if !lock.TryLock() {
		return fmt.Errorf("Minecraft %s 正在安装或启动，请稍后删除", saved.Version)
	}
	defer lock.Unlock()
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return e.removeUnusedVersion(saved)
}

func (e Engine) removeUnusedVersion(removed Instance) error {
	if !segment(removed.Version) || strings.HasPrefix(removed.Version, ".") {
		return nil
	}
	if removed.legacy {
		e.GameRoot = ""
	}
	instances, err := e.Instances()
	if err != nil {
		return err
	}
	for _, instance := range instances {
		if instance.Version == removed.Version && (e.GameRoot == "" || !instance.legacy) {
			return nil
		}
	}
	profilePath := e.baseVersionFile(removed.Version, ".json")
	dir := filepath.Dir(profilePath)
	// The versions directory can also contain instance data.
	if _, err := os.Lstat(filepath.Join(dir, "instance.json")); !os.IsNotExist(err) {
		return nil
	}
	var profile gameProfile
	if readJSON(profilePath, &profile) != nil || profile.ID != removed.Version {
		return nil
	}
	return os.RemoveAll(dir)
}

func (e Engine) instanceMetadata(instance Instance) (string, Instance, error) {
	var saved Instance
	if !segment(instance.ID) || strings.HasPrefix(instance.ID, ".") {
		return "", saved, fmt.Errorf("无效的实例")
	}
	dir := e.GameDir(instance)
	if instance.legacy || e.GameRoot == "" {
		dir = filepath.Dir(dir)
	}
	if err := readJSON(filepath.Join(dir, "instance.json"), &saved); err != nil {
		return "", saved, err
	}
	if saved.ID != instance.ID {
		return "", saved, fmt.Errorf("实例信息已更改，请刷新后重试")
	}
	return dir, saved, nil
}

func (e Engine) Install(ctx context.Context, instance Instance, status func(string)) (Instance, error) {
	return e.createInstance(ctx, instance, nil, status)
}

func (e Engine) createInstance(ctx context.Context, instance Instance, setup func(string) error, status func(string)) (_ Instance, installErr error) {
	lock := e.versionLock(instance)
	lock.RLock()
	defer func() {
		lock.RUnlock()
		if installErr == nil || !errors.Is(context.Cause(ctx), ErrTaskCanceled) || !lock.TryLock() {
			return
		}
		defer lock.Unlock()
		if err := e.removeUnusedVersion(instance); err != nil {
			installErr = errors.Join(installErr, fmt.Errorf("清理版本缓存：%w", err))
		}
	}()
	ctx = e.resumableDownloads(ctx)
	if instance.Loader != "" && instance.Build == "" {
		build, err := nextLoader(ctx, instance.Version, instance.Loader)
		if err != nil {
			return Instance{}, err
		}
		instance.Build = build
	}
	instance.Name = strings.TrimSpace(instance.Name)
	if instance.Name == "" {
		instance.Name = instance.DefaultName()
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
	instancePublishMu.Lock()
	defer instancePublishMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Instance{}, err
	}
	instance.Name, err = e.availableInstanceName(instance.Name, "")
	if err != nil {
		return Instance{}, err
	}
	var id [12]byte
	rand.Read(id[:])
	idSuffix := hex.EncodeToString(id[:])
	instance.ID = idSuffix
	if e.GameRoot != "" {
		instance.ID = instance.Version + "-Vanilla"
		if instance.Loader != "" {
			instance.ID = instance.DefaultName()
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
			readModInfo(filepath.Join(e.GameDir(instance), "mods", entry.Name()), instance.Loader, &mod)
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

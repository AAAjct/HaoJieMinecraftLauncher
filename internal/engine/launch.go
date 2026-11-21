package engine

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Account struct {
	Name, UUID string
}

type Game struct {
	Done     <-chan error
	LogPath  string
	Memory   int
	process  *os.Process
	Stopping bool
	// Crash is available after receiving from Done.
	Crash *GameCrash
}

func (g *Game) Stop() error {
	err := g.process.Kill()
	if err == nil {
		g.Stopping = true
		return nil
	}
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

var playerName = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

func LocalAccount(name string) (Account, error) {
	if !playerName.MatchString(name) {
		return Account{}, fmt.Errorf("本地玩家名需为 3–16 位字母、数字或下划线")
	}
	hash := md5.Sum([]byte("OfflinePlayer:" + name))
	hash[6] = hash[6]&0x0f | 0x30
	hash[8] = hash[8]&0x3f | 0x80
	return Account{Name: name, UUID: hex.EncodeToString(hash[:])}, nil
}

func expandArgs(raw []json.RawMessage, values *strings.Replacer) ([]string, error) {
	var args []string
	for _, entry := range raw {
		var single string
		if json.Unmarshal(entry, &single) == nil {
			args = append(args, values.Replace(single))
			continue
		}
		var conditional struct {
			Rules []restriction
			Value json.RawMessage
		}
		if err := json.Unmarshal(entry, &conditional); err != nil {
			return nil, err
		}
		if !permitted(conditional.Rules, nil) {
			continue
		}
		if json.Unmarshal(conditional.Value, &single) == nil {
			args = append(args, values.Replace(single))
			continue
		}
		var array []string
		if err := json.Unmarshal(conditional.Value, &array); err != nil {
			return nil, err
		}
		for _, arg := range array {
			args = append(args, values.Replace(arg))
		}
	}
	return args, nil
}

func expandJVMArgs(raw []json.RawMessage, values *strings.Replacer, versionName, clientJar string) ([]string, error) {
	args, err := expandArgs(raw, values)
	if err != nil {
		return nil, err
	}
	const prefix = "-DignoreList="
	for i, arg := range args {
		list, ok := strings.CutPrefix(arg, prefix)
		if !ok {
			continue
		}
		// BootstrapLauncher matches the actual classpath filenames.
		entries := strings.Split(list, ",")
		for j, entry := range entries {
			if entry == versionName+".jar" {
				entries[j] = clientJar
			}
		}
		args[i] = prefix + strings.Join(entries, ",")
	}
	return args, nil
}

type LaunchOptions struct {
	Memory     int
	Additional bool
	Skin       string
	SkinPNG    []byte
}

func (e Engine) Launch(ctx context.Context, instance Instance, account Account, options LaunchOptions, status func(string)) (*Game, error) {
	memory := options.Memory
	lock := e.versionLock(instance)
	lock.RLock()
	defer lock.RUnlock()
	ctx = e.resumableDownloads(ctx)
	if instance.legacy {
		e.GameRoot = ""
	}
	if account.Name == "" || account.UUID == "" {
		return nil, fmt.Errorf("请先设置玩家账号")
	}
	if memory < 1024 || memory > 32768 {
		return nil, fmt.Errorf("内存上限须为 1024–32768 MiB")
	}
	instanceDir := e.GameDir(instance)
	if _, err := os.Stat(instanceDir); err != nil {
		return nil, err
	}
	minimumJava, mod, err := fabricJavaMinimum(ctx, instanceDir, instance.Loader)
	if err != nil {
		return nil, err
	}
	if minimumJava > 0 {
		if _, err := launcherJavaMajor(minimumJava); err != nil {
			return nil, fmt.Errorf("模组 %s 要求 Java %d 或更高版本：%w", mod, minimumJava, err)
		}
		status(fmt.Sprintf("模组 %s 要求 Java %d 或更高版本…", mod, minimumJava))
	}
	profile, javaHome, err := e.prepareGame(ctx, instance, minimumJava, status)
	if err != nil {
		return nil, err
	}
	files, classpath, archives, err := e.gameTransfers(profile)
	if err != nil {
		return nil, err
	}
	if err := runParallel(ctx, status, func(ctx context.Context, status func(string)) error {
		return ensureAll(ctx, files, librariesStage, status)
	}, func(ctx context.Context, status func(string)) error {
		return e.downloadAssets(ctx, profile, status)
	}); err != nil {
		return nil, err
	}
	game := instanceDir
	started := false
	var session *gameSession
	if options.Additional {
		session, err = startGameSession(instanceDir)
		if err != nil {
			return nil, err
		}
		game = session.dir
		defer func() {
			if !started {
				session.close()
			}
		}()
	}
	if options.Skin != "" {
		status("正在应用离线玩家皮肤…")
		if err := applyPlayerSkin(e.baseVersionFile(instance.Version, ".jar"), game, account, options); err != nil {
			return nil, err
		}
	}
	native, err := os.MkdirTemp(game, ".natives-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if !started {
			os.RemoveAll(native)
		}
	}()
	for _, archive := range archives {
		if err := extractNatives(ctx, archive, native); err != nil {
			return nil, err
		}
	}

	assets := filepath.Join(e.gameRoot(), "assets")
	gameAssets, err := legacyAssets(assets, game, profile.AssetIndex.ID)
	if err != nil {
		return nil, err
	}
	versionName := instance.Version
	if profile.LaunchID != "" {
		versionName = profile.LaunchID
	}
	values := strings.NewReplacer(
		"${auth_player_name}", account.Name, "${auth_uuid}", account.UUID,
		"${auth_access_token}", "0", "${auth_session}", "0",
		"${auth_client_id}", "", "${clientid}", "", "${auth_xuid}", "",
		"${user_type}", "legacy", "${user_properties}", "{}",
		"${version_name}", versionName, "${version_type}", "HJML",
		"${game_directory}", game, "${assets_root}", assets, "${game_assets}", gameAssets,
		"${assets_index_name}", profile.AssetIndex.ID, "${natives_directory}", native,
		"${launcher_name}", "HJML", "${launcher_version}", "1.0.8",
		"${library_directory}", filepath.Join(e.gameRoot(), "libraries"), "${classpath_separator}", ";",
		"${classpath}", strings.Join(classpath, ";"),
	)
	clientJar := filepath.Base(e.baseVersionFile(profile.ID, ".jar"))
	jvmArgs, err := expandJVMArgs(profile.Arguments.JVM, values, versionName, clientJar)
	if err != nil {
		return nil, err
	}
	if len(jvmArgs) == 0 {
		jvmArgs = []string{"-Djava.library.path=" + native, "-cp", strings.Join(classpath, ";")}
	}
	jvmArgs = append(jvmArgs, "-XX:ErrorFile="+filepath.Join(instanceDir, "hs_err_pid%p.log"))
	if file := profile.Logging.Client.File; file.ID != "" && profile.Logging.Client.Argument != "" {
		jvmArgs = append(jvmArgs, strings.ReplaceAll(profile.Logging.Client.Argument, "${path}",
			filepath.Join(assets, "log_configs", file.ID)))
	}
	gameArgs, err := expandArgs(profile.Arguments.Game, values)
	if err != nil {
		return nil, err
	}
	if profile.MinecraftArguments != "" {
		old := strings.Fields(profile.MinecraftArguments)
		for i := range old {
			old[i] = values.Replace(old[i])
		}
		gameArgs = append(old, gameArgs...)
	}
	args := append([]string{"-Xmx" + strconv.Itoa(memory) + "M"}, jvmArgs...)
	args = append(args, profile.MainClass)
	args = append(args, gameArgs...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var log *os.File
	if options.Additional {
		log, err = os.CreateTemp(instanceDir, "hjml-launch-*.log")
	} else {
		log, err = os.Create(filepath.Join(game, "hjml-latest.log"))
	}
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(javaHome, "bin", "java.exe"), args...)
	cmd.Dir = game
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = append(os.Environ(), "JAVA_HOME="+javaHome)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsNoConsole}
	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	started = true
	done := make(chan error, 1)
	running := &Game{Done: done, LogPath: log.Name(), Memory: memory, process: cmd.Process}
	go func() {
		err := cmd.Wait()
		log.Close()
		running.Crash = diagnoseGameExit(err, running.LogPath, instanceDir, game, cmd.Process.Pid, startedAt)
		os.RemoveAll(native)
		if session != nil {
			session.close()
		}
		done <- err
		close(done)
	}()
	return running, nil
}

const windowsNoConsole = 0x08000000

func extractNatives(ctx context.Context, archive, directory string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, entry := range z.File {
		if !strings.HasSuffix(strings.ToLower(entry.Name), ".dll") || !entry.Mode().IsRegular() {
			continue
		}
		name := filepath.Base(entry.Name)
		if !segment(name) {
			return fmt.Errorf("无效 Windows 原生库")
		}
		if err := writeZipEntry(ctx, entry, filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(ctx context.Context, entry *zip.File, target string) error {
	if !entry.Mode().IsRegular() || entry.UncompressedSize64 > 512<<20 {
		return fmt.Errorf("不支持的压缩条目 %s", entry.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, cancellableReader{ctx, source})
	closeErr := output.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func legacyAssets(root, game, id string) (string, error) {
	var index assetIndex
	if err := readJSON(filepath.Join(root, "indexes", id+".json"), &index); err != nil {
		return "", err
	}
	if !index.Virtual && !index.MapToResources {
		return root, nil
	}
	target := filepath.Join(root, "virtual", id)
	if index.MapToResources {
		target = filepath.Join(game, "resources")
	}
	for name, entry := range index.Objects {
		path, err := localPath(target, name)
		if err != nil || !validAssetHash(entry.Hash) {
			return "", fmt.Errorf("无效旧版资源索引")
		}
		if regularFile(path) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return "", err
		}
		if err := os.Link(filepath.Join(root, "objects", entry.Hash[:2], entry.Hash), path); err != nil && !regularFile(path) {
			return "", err
		}
	}
	return target, nil
}

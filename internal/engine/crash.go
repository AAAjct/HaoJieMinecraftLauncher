package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

type GameCrash struct {
	Reason, Detail      string
	Mods                []string
	ExitCode            int
	LogPath, ReportPath string
	LogText, ReportText string
}

var crashReportName = regexp.MustCompile(`(?i)crash-\d{4}-\d{2}-\d{2}_\d{2}\.\d{2}\.\d{2}-[a-z0-9_-]+\.txt`)
var modInError = regexp.MustCompile(`(?im)^(?:[^\n]*(?:mixin apply for mod|mixin[^\n]* from mod)) ([a-z0-9_-]+)\b[^\n]*(?:failed|exception|error)`)
var javaException = regexp.MustCompile(`\b[\w.$]+(?:Exception|Error):`)
var missingModDependency = regexp.MustCompile(`(?im)\bmod [a-z0-9_-]+ requires \S+`)
var fabricJavaDependency = regexp.MustCompile(`\bHARD_DEP(?:_INCOMPATIBLE_PRESELECTED)? ([a-z0-9_-]+) [^\s{}]+ \{depends java @ \[>=(\d+)\]\}`)
var crashJavaVersion = regexp.MustCompile(`(?im)\bjava version:\s*(?:1\.)?(\d+)`)
var fabricJavaFix = regexp.MustCompile(`\[java (\d+)\] -> add:java \d+`)

const unknownCrashReason = "游戏异常退出，现有日志无法确定具体原因。"

func diagnoseGameExit(exitErr error, logPath, instanceDir, gameDir string, pid int, started time.Time) *GameCrash {
	latest := readCrashText(filepath.Join(gameDir, "logs", "latest.log"), started, true)
	console := readCrashText(logPath, started, true)
	crash := &GameCrash{LogPath: logPath, LogText: console}
	if crash.LogText == "" {
		crash.LogText = latest
	}
	var exit *exec.ExitError
	if errors.As(exitErr, &exit) {
		crash.ExitCode = int(int32(exit.ExitCode()))
	}
	var reported bool
	lines := strings.Split(latest+"\n"+console, "\n")
	for _, line := range slices.Backward(lines) {
		if !strings.Contains(strings.ToLower(line), "crash report saved to") {
			continue
		}
		reported = true
		if name := crashReportName.FindString(line); name != "" {
			path := filepath.Join(instanceDir, "crash-reports", name)
			if report := readCrashText(path, started, false); report != "" {
				crash.ReportPath, crash.ReportText = path, report
				break
			}
		}
	}
	if crash.ReportText == "" {
		path := filepath.Join(instanceDir, fmt.Sprintf("hs_err_pid%d.log", pid))
		if report := readCrashText(path, started, false); report != "" {
			crash.ReportPath, crash.ReportText = path, report
		}
	}
	if exitErr == nil && !reported && crash.ReportText == "" {
		return nil
	}
	evidence := latest + "\n" + console + "\n" + crash.ReportText
	crash.Reason = crashReason(crash.ReportText, crash.ExitCode)
	if crash.Reason == unknownCrashReason {
		crash.Reason = crashReason(evidence, crash.ExitCode)
	}
	crash.Detail = crashDetail(crash.ReportText)
	if crash.Detail == "" {
		crash.Detail = crashDetail(latest + "\n" + console)
	}
	if exitErr != nil && exit == nil && crash.Detail == "" {
		crash.Detail = exitErr.Error()
	}
	crash.Mods = crashMods(crash.ReportText, evidence)
	if reason, detail, mod := fabricJavaCrash(evidence); reason != "" {
		crash.Reason, crash.Detail = reason, detail
		if !slices.Contains(crash.Mods, mod) {
			crash.Mods = append([]string{mod}, crash.Mods...)
		}
	}
	return crash
}

func fabricJavaCrash(text string) (reason, detail, mod string) {
	version := crashJavaVersion.FindStringSubmatch(text)
	if version == nil {
		version = fabricJavaFix.FindStringSubmatch(text)
	}
	if version == nil {
		return "", "", ""
	}
	running, _ := strconv.Atoi(version[1])
	var highest int
	for _, match := range fabricJavaDependency.FindAllStringSubmatch(text, -1) {
		required, err := strconv.Atoi(match[2])
		if err == nil && required > max(running, highest) {
			highest, detail, mod = required, match[0], match[1]
		}
	}
	if highest > 0 {
		reason = fmt.Sprintf("模组 %s 至少需要 Java %d，本次启动使用的是 Java %d，模组加载因此失败。", mod, highest, running)
	}
	return reason, detail, mod
}

func readCrashText(path string, started time.Time, tail bool) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(started) {
		return ""
	}
	const limit = 256 << 10
	if tail && info.Size() > limit {
		if _, err := file.Seek(-limit, io.SeekEnd); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return ""
	}
	if !utf8.Valid(data) {
		decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
		if err == nil {
			data = decoded
		}
	}
	return strings.ToValidUTF8(string(data), "�")
}

func crashReason(evidence string, code int) string {
	text := strings.ToLower(evidence)
	switch {
	case strings.Contains(text, "outofmemoryerror"), strings.Contains(text, "could not reserve enough space"), strings.Contains(text, "there is insufficient memory for the java runtime"):
		return "Java 内存不足，游戏无法继续运行。"
	case strings.Contains(text, "unsupportedclassversionerror"):
		return "当前 Java 版本与游戏或模组要求的版本不匹配。"
	case strings.Contains(text, "incompatible mods"), strings.Contains(text, "modresolutionexception"),
		strings.Contains(text, "missing or unsupported mandatory dependencies"), missingModDependency.MatchString(evidence):
		return "模组版本不兼容，或缺少必需的前置模组。"
	case strings.Contains(text, "mixinapplyerror"), strings.Contains(text, "mixintransformererror"),
		strings.Contains(text, "invalidmixinexception"), strings.Contains(text, "invalidinjectionexception"),
		strings.Contains(text, "injectionerror"), modInError.MatchString(evidence):
		return "模组的 Mixin 注入失败，可能存在模组冲突或版本不匹配。"
	case strings.Contains(text, "glfw error"), strings.Contains(text, "failed to create window"), strings.Contains(text, "does not support opengl"):
		return "游戏无法初始化图形窗口，可能与显卡驱动或 OpenGL 支持有关。"
	case strings.Contains(text, "unsatisfiedlinkerror"):
		return "游戏无法加载所需的本机运行库。"
	case strings.Contains(text, "exception_access_violation"), uint32(code) == 0xc0000005:
		return "Java 或本机组件发生内存访问异常。"
	case strings.Contains(text, "modloadingexception"), strings.Contains(text, "mod loading has failed"):
		return "游戏在加载或运行模组时发生错误。"
	case javaException.MatchString(evidence):
		return "游戏运行时发生 Java 异常。"
	default:
		return unknownCrashReason
	}
}

func crashDetail(text string) string {
	text, _, _ = strings.Cut(text, "\nA detailed walkthrough of the error")
	lines := strings.Split(text, "\n")
	for _, pass := range []func(string) bool{
		func(line string) bool { return strings.HasPrefix(line, "Caused by:") },
		func(line string) bool { return !strings.HasPrefix(line, "at ") && javaException.MatchString(line) },
		func(line string) bool {
			return strings.HasPrefix(line, "Description:") || strings.Contains(line, "EXCEPTION_ACCESS_VIOLATION")
		},
	} {
		for _, line := range slices.Backward(lines) {
			line = strings.TrimSpace(line)
			if pass(line) {
				runes := []rune(line)
				if len(runes) > 480 {
					return string(runes[:480]) + "…"
				}
				return line
			}
		}
	}
	return ""
}

func crashMods(report, evidence string) []string {
	var mods []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && len(name) <= 180 && !strings.EqualFold(name, "none") &&
			!strings.EqualFold(name, "unknown") && !strings.HasPrefix(name, "Version:") &&
			!slices.Contains(mods, name) && len(mods) < 8 {
			mods = append(mods, name)
		}
	}
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, "Suspected Mods:"); ok {
			for _, name := range strings.Split(name, ", ") {
				add(name)
			}
		}
		if name, ok := strings.CutPrefix(line, "Mod File:"); ok {
			name = filepath.Base(strings.TrimSpace(name))
			if strings.EqualFold(filepath.Ext(name), ".jar") {
				add(name)
			}
		}
	}
	for _, match := range modInError.FindAllStringSubmatch(evidence, -1) {
		add(match[1])
	}
	return mods
}

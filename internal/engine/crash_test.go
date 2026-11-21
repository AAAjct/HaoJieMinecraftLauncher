package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDiagnoseGameExit(t *testing.T) {
	instance := t.TempDir()
	game := filepath.Join(instance, ".session-test")
	crashDir := filepath.Join(instance, "crash-reports")
	for _, dir := range []string{game, crashDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now().Add(-time.Minute)
	log := filepath.Join(instance, "hjml-launch-test.log")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(log, "Stopping Minecraft\n")
	if report := diagnoseGameExit(nil, log, instance, game, 42, started); report != nil {
		t.Fatalf("normal exit produced a crash report: %+v", report)
	}

	name := "crash-2026-11-19_14.00.00-client.txt"
	path := filepath.Join(crashDir, name)
	write(path, "Description: Unexpected error\nSuspected Mods: Oculus (oculus), Version: 1.8.0\n"+
		"Mod File: C:/instance/mods/embeddium-0.3.31.jar\n"+
		"Caused by: org.spongepowered.asm.mixin.throwables.MixinApplyError: incompatible rendering mixin\n")
	write(log, "#@!@# Game crashed! Crash report saved to: #@!@# "+filepath.Join(game, "crash-reports", name))
	report := diagnoseGameExit(nil, log, instance, game, 42, started)
	if report == nil || report.ReportPath != path || !strings.Contains(report.Reason, "Mixin 注入失败") ||
		!strings.Contains(report.Detail, "incompatible rendering mixin") ||
		!slices.Equal(report.Mods, []string{"Oculus (oculus)", "embeddium-0.3.31.jar"}) {
		t.Fatalf("shared crash report was not analyzed: %+v", report)
	}

	write(log, "Java Version: 17.0.21\n[INFO] Reason: [HARD_DEP zoomify 2.14.4+1.20.1 {depends java @ [>=21]}]\n"+
		"Crash report saved to: "+filepath.Join(game, "crash-reports", name))
	report = diagnoseGameExit(nil, log, instance, game, 42, started)
	if report == nil || !strings.Contains(report.Reason, "zoomify 至少需要 Java 21") ||
		!strings.Contains(report.Reason, "本次启动使用的是 Java 17") || !strings.Contains(report.Detail, "HARD_DEP zoomify") {
		t.Fatalf("mod Java requirement should take precedence over the later crash: %+v", report)
	}

	write(log, "Caused by: java.lang.OutOfMemoryError: Java heap space\n")
	cmd := exec.CommandContext(t.Context(), "cmd.exe", "/c", "exit", "7")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windowsNoConsole}
	err := cmd.Run()
	report = diagnoseGameExit(err, log, instance, game, 42, started)
	if report == nil || report.ExitCode != 7 || report.ReportPath != "" ||
		!strings.Contains(report.Reason, "内存不足") || !strings.Contains(report.Detail, "Java heap space") {
		t.Fatalf("process exit was not analyzed: %+v, %v", report, err)
	}

	jvmPath := filepath.Join(instance, "hs_err_pid42.log")
	write(jvmPath, "# EXCEPTION_ACCESS_VIOLATION\n")
	if err := os.Chtimes(jvmPath, started.Add(-time.Hour), started.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if report := diagnoseGameExit(nil, log, instance, game, 42, started); report != nil {
		t.Fatalf("an old JVM report triggered a popup: %+v", report)
	}
}

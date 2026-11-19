package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"hjml/internal/engine"
)

func TestImportPacksProcessesEntireBatch(t *testing.T) {
	for _, paths := range [][]string{
		{"first.mrpack", "second.zip"},
		{"broken.rar", "first.mrpack", "second.zip"},
		{"first.mrpack", "broken.rar", "second.zip"},
	} {
		t.Run(strings.Join(paths, "+"), func(t *testing.T) {
			var attempted, progress []string
			installed, err := importPacks(t.Context(), splitImportFiles(paths).packs,
				func(_ context.Context, path string, status func(string)) (engine.Instance, error) {
					attempted = append(attempted, path)
					status("正在读取整合包清单…")
					if path == "broken.rar" {
						return engine.Instance{}, errors.New("测试用损坏文件")
					}
					return engine.Instance{ID: path}, nil
				}, func(message string) { progress = append(progress, message) })
			if !slices.Equal(attempted, paths) || len(installed) != 2 ||
				installed[0].ID != "first.mrpack" || installed[1].ID != "second.zip" {
				t.Fatalf("attempted %v, installed %v", attempted, installed)
			}
			if len(progress) != len(paths) || !strings.Contains(progress[len(paths)-1], "second.zip") {
				t.Fatalf("unexpected progress: %v", progress)
			}
			if len(paths) == 2 && err != nil ||
				len(paths) == 3 && (err == nil || !strings.Contains(err.Error(), "2/3") || !strings.Contains(err.Error(), "broken.rar")) {
				t.Fatalf("unexpected batch error: %v", err)
			}
		})
	}
}

func TestImportPacksStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	installed, err := importPacks(ctx, []string{"first.zip", "second.zip"},
		func(_ context.Context, path string, _ func(string)) (engine.Instance, error) {
			if path != "first.zip" {
				t.Fatalf("imported %s after cancellation", path)
			}
			cancel()
			return engine.Instance{ID: path}, nil
		}, func(string) {})
	if len(installed) != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("installed %v, error %v", installed, err)
	}
}

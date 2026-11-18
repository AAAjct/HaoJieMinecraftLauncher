package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

type importRequest struct {
	instance    engine.Instance
	mods, packs []string
}

func (a *app) receiveFiles(paths []string) {
	if a.importConfirm {
		a.status = "请先处理当前的导入确认。"
		return
	}
	request := importRequest{instance: a.current()}
	for _, path := range paths {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jar":
			request.mods = append(request.mods, path)
		case ".mrpack", ".zip":
			request.packs = append(request.packs, path)
		}
	}
	if len(request.mods) > 0 {
		if request.instance.ID == "" || a.gameInUse(request.instance) {
			a.status = "请先选中一个当前可修改的实例，再导入模组。"
			return
		}
		a.pendingImport = request
		a.importConfirm = true
	} else if len(request.packs) > 0 {
		a.installFiles(request)
	} else {
		a.status = "可拖入 JAR 模组或 Modrinth 整合包。"
	}
}

func (a *app) chooseFiles() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.window, Title: "从文件安装", Multiple: true,
			Filters: []mygo.FileFilter{{Name: "模组与整合包", Extensions: []string{"jar", "mrpack", "zip"}}},
		})
		a.window.Update(func() {
			if err != nil {
				a.status = err.Error()
			} else if len(paths) > 0 {
				a.receiveFiles(paths)
			}
		})
	}()
}

func (a *app) installFiles(request importRequest) {
	if len(request.mods) > 0 && a.gameInUse(request.instance) {
		a.status = "目标实例正在启动或运行，请稍后重试。"
		return
	}
	target := ""
	if len(request.mods) > 0 {
		target = request.instance.ID
	}
	kind := downloadJob
	if len(request.packs) == 0 {
		kind = importJob
	}
	a.startJob(&job{kind: kind, title: "从文件安装", target: target, message: "正在读取本地文件…"}, func(ctx context.Context, status func(string)) (func(), error) {
		if len(request.mods) > 0 {
			status("正在安装模组 · " + request.instance.Name)
			if err := a.engine.InstallLocalMods(ctx, request.instance, request.mods); err != nil {
				return nil, err
			}
		}
		var installed engine.Instance
		for _, path := range request.packs {
			pack, err := a.engine.ImportPack(ctx, path, status)
			if err != nil {
				return nil, fmt.Errorf("%s：%w", filepath.Base(path), err)
			}
			installed = pack
		}
		return func() {
			if installed.ID != "" {
				a.installed(installed)
			} else {
				a.refreshMods()
				a.status = fmt.Sprintf("已安装 %d 个模组 · %s", len(request.mods), request.instance.Name)
			}
		}, nil
	})
}

func (a *app) importDialog(c *ui.Context) {
	ui.Modal(c, &a.importConfirm, func() {
		request := a.pendingImport
		ui.Column(c).Width(430).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "安装本地模组").FontSize(21).Bold().Focusable().AutoFocus()
			ui.Textf(c, "是否将 %d 个模组安装至 %s（Minecraft %s）？", len(request.mods),
				request.instance.Name, request.instance.Version)
			if len(request.mods) == 1 {
				ui.Text(c, filepath.Base(request.mods[0])).TextColor(a.theme.TextMuted)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					a.importConfirm = false
				}
				if ui.PrimaryButton(c, "安装模组").Clicked() {
					a.importConfirm = false
					a.installFiles(request)
				}
			})
		})
	})
}

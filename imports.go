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

func splitImportFiles(paths []string) importRequest {
	var request importRequest
	for _, path := range paths {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jar":
			request.mods = append(request.mods, path)
		case ".mrpack", ".zip", ".rar":
			request.packs = append(request.packs, path)
		}
	}
	return request
}

func (a *app) receiveFiles(paths []string) {
	request := splitImportFiles(paths)
	request.instance = a.current()
	hasPacks := len(request.packs) > 0
	if hasPacks {
		a.installFiles(importRequest{packs: request.packs})
		request.packs = nil
	}
	if len(request.mods) > 0 {
		if a.importConfirm {
			a.status = "请先处理当前的模组导入确认。"
			return
		}
		if request.instance.ID == "" || a.gameInUse(request.instance) {
			a.status = "请先选中一个当前可修改的实例，再导入模组。"
			return
		}
		a.pendingImport = request
		a.importConfirm = true
	} else if !hasPacks {
		a.status = "可拖入 JAR 模组或 MRPACK、ZIP、RAR 整合包。"
	}
}

func (a *app) chooseMods(target engine.Instance) {
	a.chooseFiles("安装模组", mygo.FileFilter{Name: "JAR 模组", Extensions: []string{"jar"}}, func(paths []string) {
		mods := splitImportFiles(paths).mods
		if len(mods) == 0 {
			a.status = "请选择 JAR 模组文件。"
			return
		}
		a.installFiles(importRequest{instance: target, mods: mods})
	})
}

func (a *app) choosePacks() {
	a.chooseFiles("导入整合包", mygo.FileFilter{Name: "整合包", Extensions: []string{"zip", "rar", "mrpack"}}, func(paths []string) {
		packs := splitImportFiles(paths).packs
		if len(packs) == 0 {
			a.status = "请选择 ZIP、RAR 或 MRPACK 整合包。"
			return
		}
		a.installFiles(importRequest{packs: packs})
	})
}

func (a *app) chooseFiles(title string, filter mygo.FileFilter, selected func([]string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.window, Title: title, Multiple: true,
			Filters: []mygo.FileFilter{filter},
		})
		a.window.Update(func() {
			if err != nil {
				a.status = err.Error()
			} else if len(paths) > 0 {
				selected(paths)
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
		installed, err := importPacks(ctx, request.packs, a.engine.ImportPack, status)
		if err != nil {
			return nil, err
		}
		return func() {
			if len(installed) > 0 {
				a.installed(installed[len(installed)-1])
				if len(installed) > 1 {
					a.status = fmt.Sprintf("已安装 %d 个整合包", len(installed))
				}
			} else {
				a.refreshMods()
				a.status = fmt.Sprintf("已安装 %d 个模组 · %s", len(request.mods), request.instance.Name)
			}
		}, nil
	})
}

func importPacks(ctx context.Context, paths []string, importPack func(context.Context, string, func(string)) (engine.Instance, error), status func(string)) ([]engine.Instance, error) {
	var installed []engine.Instance
	var failures []string
	for index, path := range paths {
		if err := ctx.Err(); err != nil {
			return installed, err
		}
		name := filepath.Base(path)
		pack, err := importPack(ctx, path, func(message string) {
			status(fmt.Sprintf("整合包 %d/%d · %s · %s", index+1, len(paths), name, message))
		})
		if err != nil {
			if ctx.Err() != nil {
				return installed, err
			}
			failures = append(failures, fmt.Sprintf("%s：%v", name, err))
			continue
		}
		installed = append(installed, pack)
	}
	if len(failures) > 0 {
		return installed, fmt.Errorf("已安装 %d/%d 个整合包，失败：%s", len(installed), len(paths), strings.Join(failures, "；"))
	}
	return installed, nil
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

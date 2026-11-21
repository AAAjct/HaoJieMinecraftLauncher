package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

type crashNotice struct {
	instance string
	report   *engine.GameCrash
}

func (a *app) crashDialog(c *ui.Context) {
	if len(a.crashes) == 0 {
		return
	}
	notice := a.crashes[0]
	report := notice.report
	open := true
	dialog := ui.Modal(c, &open, func() {
		ui.Column(c).Key(report).Width(630).Padding(24).Gap(14).Children(func() {
			ui.Text(c, "Minecraft 异常退出").FontSize(23).Bold().TextColor(highlight).Focusable().AutoFocus()
			ui.Textf(c, "%s  ·  退出代码 %d", notice.instance, report.ExitCode).TextColor(a.theme.TextMuted)
			ui.Divider(c)
			ui.Scroll(c).MaxHeight(270).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					ui.Text(c, "原因").FontSize(15).Bold()
					ui.Text(c, report.Reason).FontSize(16)
					if len(report.Mods) > 0 {
						ui.Text(c, "可能相关的模组").FontSize(15).Bold()
						for _, mod := range report.Mods {
							ui.Text(c, mod).TextColor(a.theme.TextMuted)
						}
					}
					if report.Detail != "" {
						ui.Text(c, "原始错误").FontSize(15).Bold()
						ui.Text(c, report.Detail).TextColor(a.theme.TextMuted)
					}
				})
			})
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "查看日志").Clicked() {
					a.openFile(report.LogPath)
				}
				if ui.Button(c, "导出错误报告").Clicked() {
					a.exportCrash(notice)
				}
				if ui.Button(c, "关闭").Clicked() {
					open = false
				}
			})
		})
	})
	if dialog.Dismissed() || !open {
		a.crashes[0] = crashNotice{}
		a.crashes = a.crashes[1:]
	}
}

func (a *app) exportCrash(notice crashNotice) {
	if a.window == nil {
		return
	}
	window := a.window
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: window, Title: "导出 Minecraft 错误报告",
			DefaultPath: filepath.Join(filepath.Dir(notice.report.LogPath), "hjml-crash-report.txt"),
			Filters:     []mygo.FileFilter{{Name: "文本文件", Extensions: []string{"txt"}}},
		})
		if err == nil && path != "" {
			report := notice.report
			var text strings.Builder
			fmt.Fprintf(&text, "Minecraft 异常退出\n实例 %s\n退出代码 %d\n\n原因\n%s\n", notice.instance, report.ExitCode, report.Reason)
			if len(report.Mods) > 0 {
				fmt.Fprintf(&text, "\n可能相关的模组\n%s\n", strings.Join(report.Mods, "\n"))
			}
			if report.Detail != "" {
				fmt.Fprintf(&text, "\n原始错误\n%s\n", report.Detail)
			}
			if report.ReportText != "" {
				fmt.Fprintf(&text, "\n崩溃报告摘录\n%s\n", report.ReportText)
			}
			if report.LogText != "" {
				fmt.Fprintf(&text, "\n运行日志末尾\n%s\n", report.LogText)
			}
			err = os.WriteFile(path, []byte(text.String()), 0600)
		}
		if err != nil {
			window.Update(func() { a.status = "无法导出错误报告：" + err.Error() })
		} else if path != "" {
			window.Update(func() { a.status = "已导出错误报告 · " + filepath.Base(path) })
		}
	}()
}

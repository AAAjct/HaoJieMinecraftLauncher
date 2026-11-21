package main

import (
	"fmt"
	"strconv"

	"github.com/egoist/mygo/ui"
)

func sidebar(c *ui.Context) ui.Element {
	return ui.Column(c).Grow(1).Padding(14).Gap(8)
}

func (a *app) sideButton(c *ui.Context, label string, selected bool) bool {
	button := ui.ButtonBase(c).Label(label).Height(39).Radius(7).Padding(4, 10).AlignItems(ui.Center)
	if button.Hovered() {
		button.Background(a.theme.SurfaceHover)
	}
	color := a.theme.Text
	if selected {
		color = highlight
	}
	button.Children(func() { ui.Text(c, label).TextColor(color).FontSize(15) })
	return button.Clicked()
}

func (a *app) downloadSidebar(c *ui.Context) {
	sidebar(c).Children(func() {
		for _, category := range []string{"游戏版本", "模组", "整合包", "光影", "数据包", "资源包"} {
			if a.sideButton(c, category, category == a.resourceKind) && category != a.resourceKind {
				a.resourceKind = category
				a.leaveProject()
				if category == "游戏版本" {
					a.downloadVersions(false)
				}
			}
		}
	})
}

func (a *app) taskSidebar(c *ui.Context) {
	percentage, rate, remaining := "—", "—", "—"
	var progress, bytesPerSecond float64
	var stages, files int
	filesKnown, planning, incomplete := false, false, false
	for _, task := range a.jobs {
		if task.cancelling || (task.kind != downloadJob && task.kind != importJob && task.kind != launchJob) {
			continue
		}
		for _, stage := range task.stages {
			stages++
			planning = planning || stage.Planning
			incomplete = incomplete || stage.Fraction < 1 || stage.Remaining > 0
			progress += max(0, stage.Fraction)
			bytesPerSecond += stage.BytesPerSecond
			if stage.Remaining > 0 {
				filesKnown = true
				files += stage.Remaining
			}
		}
	}
	if stages > 0 {
		if !planning {
			total := 100 * progress / float64(stages)
			if incomplete {
				total = min(total, 99.99)
			}
			percentage = fmt.Sprintf("%.2f%%", total)
		}
		rate = fmt.Sprintf("%.0f KiB/s", bytesPerSecond/1024)
		if bytesPerSecond >= 1<<20 {
			rate = fmt.Sprintf("%.1f MiB/s", bytesPerSecond/(1<<20))
		}
		if filesKnown || !incomplete {
			remaining = strconv.Itoa(files)
		}
	}
	ui.Column(c).Grow(1).Justify(ui.Center).AlignItems(ui.Center).Gap(46).Children(func() {
		for _, row := range []struct{ label, value string }{
			{"总进度", percentage}, {"下载速度", rate}, {"剩余文件", remaining},
		} {
			ui.Column(c).AlignItems(ui.Center).Gap(8).Children(func() {
				ui.Text(c, row.label).FontSize(15).TextColor(highlight)
				ui.Row(c).Width(146).Height(1).Background(blue)
				ui.Text(c, row.value).FontSize(24).Bold()
			})
		}
	})
}

func (a *app) taskView(c *ui.Context) {
	ui.Text(c, "任务管理").FontSize(22).Bold()
	if len(a.jobs) == 0 {
		ui.Text(c, "当前没有运行中的任务。").TextColor(a.theme.TextMuted)
	}
	for _, task := range a.jobs {
		panel(c).Key(task).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, task.title).Grow(1).FontSize(16).Bold().TextColor(highlight)
				cancel := ui.ButtonBase(c).Label("取消 "+task.title).Tooltip("取消任务").Disabled(task.cancelling).
					Size(28, 28).Center().Radius(6)
				if cancel.Hovered() && !task.cancelling {
					cancel.Background(a.theme.SurfaceHover)
				}
				cancel.Children(func() { ui.Text(c, "×").FontSize(18).TextColor(highlight) })
				if cancel.Clicked() {
					task.requestCancel()
				}
			})
			if len(task.stages) == 0 || task.cancelling {
				ui.Text(c, task.message).TextColor(a.theme.TextMuted)
				return
			}
			ui.Column(c).Gap(7).Children(func() {
				for _, stage := range task.stages {
					marker := "•••"
					if stage.Fraction >= 1 && stage.Remaining == 0 {
						marker = "已完成"
					} else if stage.Fraction >= 0 {
						marker = fmt.Sprintf("%d%%", min(99, int(stage.Fraction*100)))
					}
					ui.Row(c).Key(stage.Label).Gap(12).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Width(48).Shrink(0).AlignItems(ui.Center).Children(func() {
							ui.Text(c, marker).FontSize(13).TextColor(highlight)
						})
						ui.Text(c, stage.Label).Grow(1).FontSize(14)
					})
				}
			})
		})
	}
}

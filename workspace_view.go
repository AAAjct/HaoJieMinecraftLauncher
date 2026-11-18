package main

import (
	"fmt"
	"regexp"
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
		for _, category := range []string{"游戏版本", "模组", "整合包", "光影", "数据包", "资源包", "世界"} {
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

var ratioPattern = regexp.MustCompile(`([0-9]+)/([0-9]+)`)
var percentPattern = regexp.MustCompile(`([0-9]+)%`)
var ratePattern = regexp.MustCompile(`([0-9]+\.[0-9]+ MiB/s)`)

func taskProgress(message string) (progress float64, remaining string) {
	if counts := ratioPattern.FindStringSubmatch(message); len(counts) == 3 {
		done, _ := strconv.Atoi(counts[1])
		total, _ := strconv.Atoi(counts[2])
		if total > 0 {
			return min(1, float64(done)/float64(total)), strconv.Itoa(max(0, total-done))
		}
	}
	if percent := percentPattern.FindStringSubmatch(message); len(percent) == 2 {
		value, _ := strconv.Atoi(percent[1])
		return min(1, float64(value)/100), "—"
	}
	return -1, "—"
}

func (a *app) taskSidebar(c *ui.Context) {
	sidebar(c).Children(func() {
		percentage, rate, remaining := "—", "—", "—"
		count := a.downloadCount()
		if count > 1 {
			ui.Textf(c, "%d 个下载任务进行中", count).FontSize(16).Bold()
			return
		}
		if task := a.activeJob(downloadJob); task != nil {
			progress, left := taskProgress(task.message)
			remaining = left
			if progress >= 0 {
				percentage = fmt.Sprintf("%.0f%%", progress*100)
			}
			if match := ratePattern.FindStringSubmatch(task.message); len(match) == 2 {
				rate = match[1]
			}
		}
		for _, row := range []struct{ label, value string }{
			{"当前阶段", percentage}, {"下载速率", rate}, {"待处理文件", remaining},
		} {
			ui.Column(c).Padding(12).Gap(6).Children(func() {
				ui.Text(c, row.label).FontSize(13).TextColor(a.theme.TextMuted)
				ui.Text(c, row.value).FontSize(24).Bold().TextColor(highlight)
			})
		}
	})
}

func (a *app) taskView(c *ui.Context) {
	panel(c).Children(func() {
		ui.Text(c, "任务管理").FontSize(22).Bold()
		if len(a.jobs) == 0 {
			ui.Text(c, "当前没有运行中的任务。").TextColor(a.theme.TextMuted)
		}
	})
	for _, task := range a.jobs {
		panel(c).Key(task).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, task.title).Grow(1).FontSize(16).Bold().TextColor(highlight)
				if ui.Button(c, "取消").Label("取消 " + task.title).Disabled(task.cancelling).Clicked() {
					task.requestCancel()
				}
			})
			ui.Text(c, task.message).TextColor(a.theme.TextMuted)
			progress, _ := taskProgress(task.message)
			ui.Progress(c, progress).Label(task.title + "进度")
		})
	}
}

package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

//go:embed resources/steve.png
var stevePNG []byte

var steveAvatar, _ = ui.DecodeImage(stevePNG)

var (
	white       = ui.Hex("#F5FAFF")
	blue        = ui.Hex("#246BBB")
	highlight   = ui.Hex("#93CEFF")
	danger      = ui.Hex("#FF8585")
	line        = ui.Hex("#334459")
	scrollToTop = ui.Animation{Duration: 260 * time.Millisecond}
	pageMotion  = ui.ElementTransition{
		Animation:    &ui.Animation{Duration: 220 * time.Millisecond, Delay: 65 * time.Millisecond},
		ExitDuration: 115 * time.Millisecond, ExitDelay: -1, ExitEase: ui.EaseIn,
		Position: true, Enter: &ui.Motion{Y: 12}, Exit: &ui.Motion{Y: -6},
	}
)

func panel(c *ui.Context) ui.Element {
	return ui.Column(c).Background(ui.Hex("#1A2635")).Border(1, line).Radius(10).Padding(16).Gap(10)
}

func (a *app) view(c *ui.Context) {
	c.SetTheme(a.theme)
	ui.Column(c).Fill().Background(a.theme.Background).Children(func() {
		ui.Row(c).Height(58).Padding(0, 10, 0, 20).Gap(10).Background(blue).Children(func() {
			ui.Text(c, "HJML").FontSize(21).Bold().TextColor(white).DragWindow()
			ui.Spacer(c).Grow(1).Height(48).DragWindow()
			for _, label := range []string{launchPage, downloadsPage, managePage, tasksPage} {
				button := ui.ButtonBase(c).Label(label).Center().Width(86).Height(38).Radius(7)
				if button.Hovered() {
					button.Background(ui.Hex("#3980CF"))
				}
				var selected float32
				if a.page == label {
					selected = 1
				}
				underline := button.AnimateWith("selected", selected, 150*time.Millisecond, ui.EaseOut)
				if underline > 0 {
					button.DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.Fill(ui.Rect{X: r.X + r.W/2 - 12, Y: r.Y + r.H - 2, W: 24, H: 2}, white.Alpha(underline), 1)
					})
				}
				button.Children(func() { ui.Text(c, label).FontSize(15).Bold().TextColor(white) })
				if button.Clicked() {
					a.navigate(label)
					if label == downloadsPage && a.resourceKind == "游戏版本" {
						a.downloadVersions(false)
					}
				}
			}
			ui.Spacer(c).Grow(1).Height(48).DragWindow()
			if a.window != nil {
				a.windowButtons(c)
			}
		})
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			a.pageSidebar(c)
			scrollKey := [4]string{a.page}
			if a.page == downloadsPage {
				scrollKey = [4]string{a.page, a.resourceKind, a.project.ID, a.targetVersion}
			}
			ui.Column(c).Grow(1).Clip().Children(func() {
				if a.page != launchPage {
					ui.Scroll(c).Key(scrollKey).Transition(pageMotion).TrackScroll(&a.mainScroll).Grow(1).Padding(18, 62, 18, 18).Children(func() {
						ui.Column(c).Gap(12).Children(func() {
							switch a.page {
							case downloadsPage:
								a.downloadView(c)
							case managePage:
								a.manageView(c)
							case tasksPage:
								a.taskView(c)
							}
						})
					})
				}
				if len(a.running) > 0 {
					if circleButton(c, "结束所有 Minecraft 实例", "×").Absolute().Bottom(16).Right(14).Clicked() {
						a.stopGames()
					}
				}
				if a.page != launchPage && a.mainScroll.MaxY > 1 {
					button := circleButton(c, "回到顶部", "↑").Absolute().Right(14)
					if len(a.running) > 0 {
						button.Bottom(62)
					} else {
						button.Bottom(16)
					}
					if button.Clicked() {
						a.mainScroll.ScrollTo(0, 0, scrollToTop)
					}
				}
			})
		})
		if a.page == downloadsPage && a.resourceKind == "游戏版本" && a.targetVersion != "" && a.hasVersion() {
			a.installVersionBar(c)
		}
		ui.Divider(c)
		ui.Column(c).Background(ui.Hex("#182333")).Children(func() {
			downloads := a.downloadCount()
			for _, kind := range []jobKind{downloadJob, launchJob, catalogJob} {
				task := a.activeJob(kind)
				if task == nil {
					continue
				}
				ui.Row(c).AlignItems(ui.Center).Padding(5, 16, 0, 16).Gap(9).Children(func() {
					if kind == downloadJob && downloads > 1 {
						ui.Textf(c, "%d 个下载任务进行中", downloads).Grow(1).FontSize(12).TextColor(a.theme.TextMuted)
						if ui.Button(c, "查看下载任务").Clicked() {
							a.navigate(tasksPage)
						}
						return
					}
					ui.Progress(c, -1).Width(66).Height(4).Label(string(kind) + "进度")
					ui.Text(c, task.message).Grow(1).MaxLines(1).FontSize(12).TextColor(a.theme.TextMuted).Tooltip(task.message)
					if ui.Button(c, "取消"+string(kind)).Disabled(task.cancelling).Clicked() {
						task.requestCancel()
					}
				})
			}
			ui.Row(c).MinHeight(36).Padding(4, 16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.status).Grow(1).MaxLines(2).FontSize(12).TextColor(a.theme.TextMuted).Tooltip(a.status)
			})
		})
		a.importDialog(c)
		a.deleteInstanceDialog(c)
	})
}

func (a *app) pageSidebar(c *ui.Context) {
	width, background := float32(218), ui.Hex("#202836")
	if a.page == launchPage {
		width, background = 278, ui.Hex("#202631")
	}
	sidebar := ui.Column(c).Shrink(0).Background(background).Clip()
	sidebar.Width(sidebar.AnimateWith("width", width, 220*time.Millisecond, ui.EaseInOut)).
		Children(func() {
			ui.Column(c).Key(a.page).Grow(1).Transition(pageMotion).Children(func() {
				switch a.page {
				case launchPage:
					a.launchSidebar(c)
				case downloadsPage:
					a.downloadSidebar(c)
				case tasksPage:
					a.taskSidebar(c)
				case managePage:
					a.instanceList(c)
				}
			})
		})
}

func circleButton(c *ui.Context, label, symbol string) ui.Element {
	button := ui.ButtonBase(c).Label(label).Tooltip(label).Size(36, 36).Center().Radius(18).
		Border(1, line).Background(c.Theme().Surface)
	if button.Hovered() {
		button.Background(c.Theme().SurfaceHover)
	}
	button.DrawOver(func(p *ui.Painter, r ui.Rect) {
		x, y := r.X+r.W/2, r.Y+r.H/2
		if symbol == "×" {
			p.StrokePolyline([][2]float32{{x - 5, y - 5}, {x + 5, y + 5}}, 2, white)
			p.StrokePolyline([][2]float32{{x - 5, y + 5}, {x + 5, y - 5}}, 2, white)
			return
		}
		p.Line(x, y-6, x, y+6, 2, white)
		p.StrokePolyline([][2]float32{{x - 5, y - 1}, {x, y - 6}, {x + 5, y - 1}}, 2, white)
	})
	return button
}

func (a *app) windowButtons(c *ui.Context) {
	maximize := "最大化"
	if a.window.IsMaximized() {
		maximize = "还原窗口"
	}
	for _, action := range []struct {
		label, symbol string
		click         func()
	}{
		{"最小化", "−", a.window.Minimize},
		{maximize, "□", a.window.ToggleMaximize},
		{"关闭窗口", "×", a.window.Close},
	} {
		button := ui.ButtonBase(c).Label(action.label).Tooltip(action.label).Center().Width(34).Height(34).Radius(6)
		if button.Hovered() {
			button.Background(ui.Hex("#3980CF"))
		}
		button.Children(func() { ui.Text(c, action.symbol).FontSize(17).TextColor(white) })
		if button.Clicked() {
			action.click()
		}
	}
}

func (a *app) instanceList(c *ui.Context) {
	ui.Column(c).Grow(1).Padding(15).Gap(9).Children(func() {
		ui.Text(c, "我的实例").FontSize(16).Bold()
		if len(a.instances) == 0 {
			ui.Text(c, "安装游戏版本或导入整合包后，实例会出现在这里。").
				FontSize(13).TextColor(a.theme.TextMuted)
			return
		}
		ui.Scroll(c).TrackScroll(&a.instanceScroll).Grow(1).Children(func() {
			ui.Column(c).Gap(7).Children(func() {
				for _, i := range a.instances {
					label := i.Name
					if a.running[i.ID] != nil {
						label += " · 运行中"
					}
					tooltip := i.Name + " · " + i.Version + " · " + loaderName(i)
					button := ui.Button(c, label).Key(i.ID).Tooltip(tooltip).
						Height(38).MaxLines(1)
					if i.ID == a.selected {
						button.TextColor(highlight).Bold()
					}
					if button.Clicked() {
						a.selectInstance(i)
					}
				}
			})
		})
		if a.instanceScroll.MaxY > 1 {
			if circleButton(c, "回到实例列表顶部", "↑").AlignSelf(ui.End).Clicked() {
				a.instanceScroll.ScrollTo(0, 0, scrollToTop)
			}
		}
	})
}

func (a *app) launchSidebar(c *ui.Context) {
	i := a.current()
	ui.Column(c).Grow(1).Padding(20).Gap(11).Children(func() {
		ui.Spacer(c).Grow(1)
		ui.Column(c).Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Image(c, steveAvatar).Size(64, 64).Label("史蒂夫头像")
			before := a.nickname
			input := ui.TextInput(c, &a.nickname).Label("离线玩家名").Width(172).TextAlign(ui.Center)
			if before != a.nickname || input.Submitted() {
				a.saveNickname()
			}
			ui.Text(c, "离线玩家").FontSize(13).TextColor(a.theme.TextMuted)
		})
		ui.Spacer(c).Grow(1)
		if i.ID == "" {
			ui.Text(c, "未选择实例").FontSize(13).TextColor(a.theme.TextMuted).TextAlign(ui.Center)
			if ui.PrimaryButton(c, "下载游戏").Height(44).Clicked() {
				a.openVersions()
			}
		} else {
			ui.Text(c, i.Name).FontSize(14).Bold().MaxLines(2).TextAlign(ui.Center).Tooltip(i.Name)
			label := "启动游戏"
			if a.running[i.ID] != nil {
				label = "游戏运行中"
			}
			if ui.PrimaryButton(c, label).Height(44).Disabled(a.inUse(i) || a.busy(launchJob)).Clicked() {
				a.play(i)
			}
		}
		if ui.Button(c, "实例管理").Height(36).Clicked() {
			a.navigate(managePage)
		}
	})
}

func (a *app) manageView(c *ui.Context) {
	i := a.current()
	panel(c).Children(func() {
		if i.ID == "" {
			ui.Text(c, "安装实例后可在此管理版本。").TextColor(a.theme.TextMuted)
			return
		}
		ui.Textf(c, "%s · %s", i.Version, loaderName(i)).TextColor(a.theme.TextMuted)
		a.renameInstanceView(c, i)
		folder := a.engine.GameDir(i)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.Button(c, "打开游戏目录").Clicked() {
				a.openFile(folder)
			}
			if ui.Button(c, "打开模组目录").Clicked() {
				mods := filepath.Join(folder, "mods")
				if err := os.Mkdir(mods, 0700); err != nil && !os.IsExist(err) {
					a.status = err.Error()
				} else {
					a.openFile(mods)
				}
			}
			if ui.Button(c, "刷新模组").Clicked() {
				a.status = "已刷新模组列表。"
				a.refreshMods()
			}
			if ui.Button(c, "从文件安装").Disabled(a.gameInUse(i)).Clicked() {
				a.chooseFiles()
			}
			if ui.Button(c, "删除实例").TextColor(danger).Disabled(a.inUse(i)).Clicked() {
				a.deleteConfirm = true
			}
		})
	})
	if i.ID == "" {
		return
	}
	busy := a.inUse(i)
	panel(c).Children(func() {
		ui.Textf(c, "模组 · %d", len(a.mods)).FontSize(17).Bold()
		if len(a.mods) > 0 {
			input := ui.TextInput(c, &a.modFilter).Label("搜索本地模组").Placeholder("搜索模组名称或文件名")
			if input.Submitted() {
				a.mainScroll.Y = 0
			}
		}
		if len(a.mods) == 0 {
			ui.Text(c, "该实例还没有本地模组。").TextColor(a.theme.TextMuted)
		}
		for _, mod := range a.mods {
			name := mod.Name
			if name == "" {
				name = strings.TrimSuffix(mod.Filename, ".disabled")
			}
			if chinese := a.modNames.Name(mod.ID); chinese != "" {
				name = chinese
			}
			if !matchesSearch(strings.Join([]string{name, mod.Name, mod.Version, mod.Filename}, " "), a.modFilter) {
				continue
			}
			ui.Divider(c)
			ui.Row(c).Key(mod.Filename).Gap(10).AlignItems(ui.Center).MinHeight(52).Children(func() {
				a.modRowInfo(c, mod, name)
				label := "禁用"
				if !mod.Enabled {
					label = "启用"
				}
				if ui.Button(c, label).Disabled(busy).Clicked() {
					if err := a.engine.ToggleMod(i, mod); err != nil {
						a.status = err.Error()
					} else {
						a.status = label + "模组成功。"
						a.refreshMods()
					}
				}
				if ui.Button(c, "删除").TextColor(danger).Disabled(busy).Clicked() {
					if err := a.engine.RemoveMod(i, mod); err != nil {
						a.status = err.Error()
					} else {
						a.status = "模组已删除。"
						a.refreshMods()
					}
				}
			})
		}
	})
}

func (a *app) modRowInfo(c *ui.Context, mod engine.Mod, name string) {
	if icon := a.modIcons[mod.Filename]; icon != nil {
		ui.Image(c, icon).Size(34, 34).Shrink(0).Label(name + " 图标")
	} else {
		ui.Column(c).Size(34, 34).Shrink(0).Radius(6).Background(a.theme.Surface).Center().
			Label(name + " 图标").Children(func() { ui.Text(c, "◇").TextColor(a.theme.TextMuted) })
	}
	ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			title := ui.Text(c, name).MaxLines(1).MinWidth(0).FontSize(15).Bold().Tooltip(mod.Filename)
			if !mod.Enabled {
				title.TextColor(a.theme.TextMuted).Strikethrough()
			}
			if mod.Version != "" {
				ui.Text(c, "|  "+mod.Version).MaxLines(1).FontSize(13).TextColor(a.theme.TextMuted)
			}
		})
		details := strings.TrimSuffix(strings.TrimSuffix(mod.Filename, ".disabled"), ".jar")
		if mod.Description != "" {
			details += ": " + strings.Join(strings.Fields(mod.Description), " ")
		}
		if len(mod.Tags) > 0 {
			details = strings.Join(mod.Tags, " · ") + "  " + details
		}
		ui.Text(c, details).MaxLines(1).FontSize(12).TextColor(a.theme.TextMuted).Tooltip(details)
	})
}

func (a *app) renameInstanceView(c *ui.Context, instance engine.Instance) {
	ui.Row(c).Key(instance.ID).Gap(8).Children(func() {
		input := ui.TextInput(c, &a.renameValue).Label("实例名称").Grow(1)
		if ui.PrimaryButton(c, "保存名称").Clicked() || input.Submitted() {
			if err := a.engine.RenameInstance(instance, a.renameValue); err != nil {
				a.status = err.Error()
				return
			}
			a.refreshInstances()
			a.status = "已修改实例名称。"
		}
	})
}

func (a *app) deleteInstanceDialog(c *ui.Context) {
	ui.Modal(c, &a.deleteConfirm, func() {
		i := a.current()
		ui.Column(c).Width(420).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "删除实例").FontSize(21).Bold().Focusable().AutoFocus()
			ui.Textf(c, "删除「%s」及其中的存档、模组和配置？", i.Name)
			ui.Row(c).Justify(ui.End).Gap(8).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.deleteConfirm = false
				}
				if ui.Button(c, "确认删除实例").TextColor(danger).Disabled(a.inUse(i)).Clicked() {
					a.deleteConfirm = false
					if err := a.engine.RemoveInstance(i); err != nil {
						a.refreshInstances()
						a.status = err.Error()
						return
					}
					a.modFilter = ""
					a.refreshInstances()
					a.settings.Instance = a.selected
					a.status = "已删除实例。"
					if err := a.engine.SaveSettings(a.settings); err != nil {
						a.status = err.Error()
					}
				}
			})
		})
	})
}

func versionTypeName(kind string) string {
	switch kind {
	case "release":
		return "正式版"
	case "beta":
		return "测试版"
	case "alpha":
		return "预览版"
	}
	return ""
}

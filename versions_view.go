package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

type loaderKey struct{ game, loader string }

var gameVersionGroups = []struct{ key, label string }{
	{"release", "正式版"}, {"snapshot", "预览版"}, {"old", "远古版"},
}

var loaderOptions = []struct {
	id, name, mark string
	color          ui.Color
}{
	{"forge", "Forge", "F", ui.Hex("#62594C")},
	{"fabric", "Fabric", "F", ui.Hex("#756348")},
	{"quilt", "Quilt", "Q", ui.Hex("#62527A")},
}

var versionListMotion = ui.ElementTransition{
	Duration: 180 * time.Millisecond, Height: true,
	Enter: &ui.Motion{Collapse: true}, Exit: &ui.Motion{Collapse: true},
}

func gameVersionGroup(kind string) string {
	switch kind {
	case "old_alpha", "old_beta":
		return "old"
	default:
		return kind
	}
}

func (a *app) selectGame(version string) {
	if a.targetVersion == version {
		return
	}
	a.targetVersion = version
	a.selectedLoader, a.selectedBuild = "", ""
	clear(a.openLoaders)
	a.pageChanged = time.Now()
	a.mainScroll.Y = 0
	a.preloadBuilds(version)
}

func (a *app) installSelection() engine.Instance {
	return engine.Instance{
		Name: strings.TrimSpace(a.instanceName), Version: a.targetVersion,
		Loader: a.selectedLoader, Build: a.selectedBuild,
	}
}

func (a *app) optionRow(c *ui.Context, name, description, label, mark string, color ui.Color) bool {
	button := ui.ButtonBase(c).Label(label).Height(50).Radius(8).Padding(5, 9).AlignItems(ui.Stretch)
	if button.Hovered() {
		button.Background(a.theme.SurfaceHover)
	}
	button.Children(func() {
		ui.Row(c).Gap(11).Grow(1).Children(func() {
			ui.Column(c).Width(32).Height(32).Radius(8).Background(color).Center().Children(func() {
				ui.Text(c, mark).FontSize(15).Bold().TextColor(white)
			})
			ui.Column(c).Gap(2).Grow(1).Children(func() {
				ui.Text(c, name).FontSize(15).Bold().MaxLines(1)
				if description != "" {
					ui.Text(c, description).FontSize(12).TextColor(a.theme.TextMuted).MaxLines(1)
				}
			})
			ui.Text(c, "›").FontSize(18).TextColor(highlight)
		})
	})
	return button.Clicked()
}

func releaseDate(version engine.Version) string {
	if version.ReleaseTime.IsZero() {
		return ""
	}
	return version.ReleaseTime.Local().Format("2006/01/02 15:04")
}

func (a *app) versionsView(c *ui.Context) {
	if a.targetVersion != "" && a.hasVersion() {
		a.installVersionView(c)
		return
	}
	panel(c).Children(func() {
		ui.Text(c, "Minecraft 版本").FontSize(21).Bold()
		ui.Row(c).Gap(8).Children(func() {
			previous := a.versionFilter
			input := ui.TextInput(c, &a.versionFilter).Grow(1).Label("搜索游戏版本").Placeholder("搜索版本，如 1.21.1")
			if previous != a.versionFilter || input.Submitted() {
				a.mainScroll.Y = 0
				clear(a.versionGroups)
				a.versionGroups["release"] = true
				if strings.TrimSpace(a.versionFilter) != "" {
					for _, group := range gameVersionGroups {
						a.versionGroups[group.key] = true
					}
				}
			}
			if ui.Button(c, "刷新版本").Disabled(a.versionLoading).Clicked() {
				a.downloadVersions(true)
			}
		})
	})
	if len(a.versionList) == 0 {
		panel(c).Children(func() {
			message := "刷新以获取 Mojang 版本清单。"
			if a.versionLoading {
				message = "正在获取 Minecraft 版本清单…"
			}
			ui.Text(c, message).TextColor(a.theme.TextMuted)
		})
		return
	}
	filter := strings.ToLower(strings.TrimSpace(a.versionFilter))
	if filter == "" {
		panel(c).Children(func() {
			ui.Text(c, "最新版本").FontSize(17).Bold()
			for _, kind := range gameVersionGroups[:2] {
				for _, v := range a.versionList {
					if v.Type != kind.key {
						continue
					}
					subtitle := "最新" + kind.label
					if date := releaseDate(v); date != "" {
						subtitle += " · " + date
					}
					if a.optionRow(c, v.ID, subtitle, "选择最新"+kind.label+" "+v.ID, "MC", ui.Hex("#315D70")) {
						a.selectGame(v.ID)
					}
					break
				}
			}
		})
	}
	grouped := map[string][]engine.Version{}
	typeMatches := map[string]bool{}
	for _, group := range gameVersionGroups {
		typeMatches[group.key] = matchesSearch(group.label, filter)
	}
	for _, v := range a.versionList {
		key := gameVersionGroup(v.Type)
		if typeMatches[key] || strings.Contains(strings.ToLower(v.ID), filter) {
			grouped[key] = append(grouped[key], v)
		}
	}
	found := false
	for _, group := range gameVersionGroups {
		versions := grouped[group.key]
		if len(versions) == 0 {
			continue
		}
		found = true
		panel(c).Key(group.key).Children(func() {
			expanded := a.versionGroups[group.key]
			title := fmt.Sprintf("%s（%d）", group.label, len(versions))
			if accordionHeader(c, title, expanded, false) {
				a.versionGroups[group.key] = !expanded
			}
			if !expanded {
				return
			}
			ui.Column(c).Key("minecraft-rows").Gap(3).Transition(versionListMotion).Children(func() {
				for _, version := range versions {
					if a.optionRow(c, version.ID, releaseDate(version), "选择 Minecraft "+version.ID, "MC", ui.Hex("#315D70")) {
						a.selectGame(version.ID)
					}
				}
			})
		})
	}
	if !found {
		panel(c).Children(func() { ui.Text(c, "没有匹配的版本。").TextColor(a.theme.TextMuted) })
	}
}

func accordionHeader(c *ui.Context, title string, expanded, disabled bool) bool {
	arrow := "⌄"
	if expanded {
		arrow = "⌃"
	}
	header := ui.ButtonBase(c).Label(title).Disabled(disabled).AlignItems(ui.Stretch).Height(36).Padding(5, 9).Radius(7)
	if header.Hovered() && !disabled {
		header.Background(c.Theme().SurfaceHover)
	}
	header.Children(func() {
		ui.Row(c).Grow(1).Gap(8).Children(func() {
			ui.Text(c, title).Bold().FontSize(15).Grow(1)
			ui.Text(c, arrow).FontSize(18).TextColor(highlight)
		})
	})
	return header.Clicked()
}

func (a *app) installVersionView(c *ui.Context) {
	panel(c).Children(func() {
		ui.Row(c).Gap(10).Children(func() {
			if ui.Button(c, "返回版本列表").Clicked() {
				a.selectGame("")
			}
			ui.Text(c, "Minecraft "+a.targetVersion).FontSize(20).Bold()
		})
		ui.TextInput(c, &a.instanceName).Placeholder("实例名称（可选）")
	})
	if a.targetVersion == "" {
		return
	}
	for _, loader := range loaderOptions {
		panel(c).Key(loader.id).Children(func() {
			key := loaderKey{a.targetVersion, loader.id}
			builds, loaded := a.loaderLists[key]
			expanded := a.openLoaders[loader.id]
			title := loader.name
			if a.selectedLoader == loader.id {
				title += " · 已选 " + a.selectedBuild
			} else if loaded {
				title += fmt.Sprintf("（%d）", len(builds))
			}
			if accordionHeader(c, title, expanded, false) {
				a.openLoaders[loader.id] = !expanded
				if !expanded {
					a.loadBuilds(key)
				}
			}
			if !expanded {
				return
			}
			ui.Column(c).Key("loader-rows").Gap(3).Transition(versionListMotion).Children(func() {
				if !loaded {
					if a.buildLoading[key] {
						ui.Text(c, "正在读取构建列表…").TextColor(a.theme.TextMuted)
					} else {
						if message := a.buildErrors[key]; message != "" {
							ui.Text(c, message).FontSize(12).TextColor(a.theme.TextMuted)
						}
						if ui.Button(c, "重试 "+loader.name+" 查询").AlignSelf(ui.Start).Clicked() {
							a.loadBuilds(key)
						}
					}
					return
				}
				if len(builds) == 0 {
					ui.Text(c, "这个 Minecraft 版本没有可用构建。").TextColor(a.theme.TextMuted)
					return
				}
				for _, build := range builds {
					description := "预发布"
					if build.Recommended {
						description = "推荐版本"
					} else if build.Stable {
						description = "稳定版"
					}
					if a.selectedLoader == loader.id && a.selectedBuild == build.Version {
						description = "已选 · " + description
					}
					if a.optionRow(c, build.Version, description, "选择 "+loader.name+" "+build.Version, loader.mark, loader.color) {
						a.selectedLoader, a.selectedBuild = loader.id, build.Version
					}
				}
			})
		})
	}
}

func (a *app) installVersionBar(c *ui.Context) {
	ui.Divider(c)
	ui.Row(c).Gap(10).Padding(8, 18).Background(ui.Hex("#202D3F")).Children(func() {
		selected := "原版"
		if a.selectedLoader != "" {
			selected = loaderName(engine.Instance{Loader: a.selectedLoader}) + " " + a.selectedBuild
		}
		ui.Text(c, "Minecraft "+a.targetVersion+" · "+selected).Grow(1).Bold().TextColor(highlight)
		if a.selectedLoader != "" && ui.Button(c, "改用原版").Clicked() {
			a.selectedLoader, a.selectedBuild = "", ""
		}
		if ui.PrimaryButton(c, "开始下载").Width(132).Height(38).Disabled(a.busy(downloadJob)).Clicked() {
			a.install()
		}
	})
}

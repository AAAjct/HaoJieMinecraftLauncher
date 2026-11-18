package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

type resourceGroup struct {
	key   loaderKey
	files []engine.ResourceVersion
}

func groupResourceVersions(versions []engine.ResourceVersion, catalog []engine.Version) []resourceGroup {
	var groups []resourceGroup
	index := map[loaderKey]int{}
	for _, version := range versions {
		loaders := version.Loaders
		if len(loaders) == 0 {
			loaders = []string{""}
		}
		for _, game := range version.GameVersions {
			for _, loader := range loaders {
				key := loaderKey{game, loader}
				i, exists := index[key]
				if !exists {
					i = len(groups)
					index[key] = i
					groups = append(groups, resourceGroup{key: key})
				}
				groups[i].files = append(groups[i].files, version)
			}
		}
	}
	order := map[string]int{}
	for i, version := range catalog {
		order[version.ID] = i + 1
	}
	slices.SortStableFunc(groups, func(a, b resourceGroup) int {
		x, y := order[a.key.game], order[b.key.game]
		if x == 0 {
			x = len(catalog) + 1
		}
		if y == 0 {
			y = len(catalog) + 1
		}
		return x - y
	})
	return groups
}

func resourceMatches(kind string, i engine.Instance, v engine.ResourceVersion) bool {
	if i.ID == "" || !slices.Contains(v.GameVersions, i.Version) {
		return false
	}
	// Shader engines are installed in Minecraft and are independent of the instance's mod loader.
	return kind == "shader" || (i.Loader != "" && slices.Contains(v.Loaders, i.Loader))
}

func (a *app) fetchProjectIcons(projects []engine.Project) {
	for _, project := range projects {
		source := project.IconURL
		if _, cached := a.projectIcons[source]; source == "" || cached {
			continue
		}
		a.projectIcons[source] = nil
		go func() {
			ctx, cancel := context.WithTimeout(a.catalogContext, 15*time.Second)
			defer cancel()
			data, err := engine.ProjectIcon(ctx, source)
			if err != nil {
				return
			}
			icon, err := ui.DecodeBitmapScaled(data, 96)
			if err == nil && a.catalogContext.Err() == nil {
				a.window.Update(func() { a.projectIcons[source] = icon })
			}
		}()
	}
}

func (a *app) projectIcon(c *ui.Context, project engine.Project, size float32) {
	if icon := a.projectIcons[project.IconURL]; icon != nil {
		ui.Image(c, icon).Size(size, size).Shrink(0).Radius(8).Label(project.Title)
		return
	}
	initial := "?"
	for _, letter := range project.Title {
		initial = strings.ToUpper(string(letter))
		break
	}
	ui.Column(c).Size(size, size).Shrink(0).Radius(8).Background(a.theme.Surface).Center().Children(func() {
		ui.Text(c, initial).FontSize(size / 2).Bold().TextColor(highlight)
	})
}

func resourceLoaderName(loader string) string {
	switch loader {
	case "iris":
		return "Iris"
	case "optifine":
		return "OptiFine"
	case "neoforge":
		return "NeoForge"
	case "", "minecraft":
		return "原版"
	}
	for _, option := range loaderOptions {
		if option.id == loader {
			return option.name
		}
	}
	return loader
}

func resourceFilter(c *ui.Context, label string, value *string, options []string, display func(string) string, disabled bool) bool {
	labels := []string{"全部"}
	selected := labels[0]
	for _, id := range options {
		name := display(id)
		labels = append(labels, name)
		if id == *value {
			selected = name
		}
	}
	if !ui.Select(c, &selected, labels).Grow(1).Label(label).Disabled(disabled).Changed() {
		return false
	}
	before := *value
	*value = ""
	if index := slices.Index(labels, selected); index > 0 {
		*value = options[index-1]
	}
	return before != *value
}

func (a *app) downloadView(c *ui.Context) {
	if a.resourceKind == "游戏版本" {
		a.versionsView(c)
		return
	}
	if a.project.ID != "" {
		a.resourceDetails(c)
		return
	}
	state := a.resourceSearches[a.kind()]
	if !state.loaded && !state.failed && !a.busy(catalogJob) {
		a.search(0)
	}
	busy := a.busy(catalogJob)
	panel(c).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			input := ui.TextInput(c, &state.query).Key(a.kind()).Label("搜索" + a.resourceKind).
				Placeholder("搜索" + a.resourceKind + "，支持拼音").Grow(1).Disabled(busy)
			if ui.PrimaryButton(c, "搜索").Width(72).Disabled(busy).Clicked() || input.Submitted() {
				a.submitResourceSearch(state)
			}
		})
		var games, loaders []string
		for _, v := range a.versionList {
			games = append(games, v.ID)
		}
		for _, loader := range a.resourceLoaders {
			if slices.Contains(loader.ProjectTypes, a.kind()) {
				loaders = append(loaders, loader.Name)
			}
		}
		if len(loaders) == 0 {
			loaders = []string{"fabric", "forge", "neoforge", "quilt"}
			if a.kind() == "shader" {
				loaders = []string{"iris", "optifine"}
			}
		}
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, "游戏版本").FontSize(13)
			gameChanged := resourceFilter(c, "筛选游戏版本", &state.game, games, func(s string) string { return s }, busy)
			ui.Text(c, "加载器").FontSize(13)
			loaderChanged := resourceFilter(c, "筛选加载器", &state.loader, loaders, resourceLoaderName, busy)
			if gameChanged || loaderChanged {
				a.submitResourceSearch(state)
			}
		})
	})
	if !state.loaded && !state.failed {
		return
	}
	panel(c).Children(func() {
		if state.loaded && len(state.results) == 0 {
			ui.Text(c, "没有匹配的项目。").FontSize(14).TextColor(a.theme.TextMuted)
		}
		for _, project := range state.results {
			a.resourceRow(c, project, busy)
		}
		if state.failed {
			if ui.Button(c, "重试搜索").AlignSelf(ui.Start).Disabled(busy).Clicked() {
				a.search(state.requested)
			}
		}
	})
	pages := (state.total + engine.ResourcePageSize - 1) / engine.ResourcePageSize
	if pages > 1 {
		ui.Row(c).AlignSelf(ui.Center).Gap(8).Padding(5, 10).Radius(8).Background(a.theme.Surface).Children(func() {
			if ui.Button(c, "«").Label("第一页").Disabled(busy || state.page == 0).Clicked() {
				a.search(0)
			}
			if ui.Button(c, "‹").Label("上一页").Disabled(busy || state.page == 0).Clicked() {
				a.search(state.page - 1)
			}
			ui.Textf(c, "%d / %d", state.page+1, pages).FontSize(13).TextColor(highlight)
			if ui.Button(c, "›").Label("下一页").Disabled(busy || state.page+1 >= pages).Clicked() {
				a.search(state.page + 1)
			}
		})
	}
}

func (a *app) resourceRow(c *ui.Context, project engine.Project, busy bool) {
	name := bilingualName(project.Title, project.Slug)
	row := ui.ButtonBase(c).Key(project.ID).Label(name).Disabled(busy).AlignItems(ui.Stretch).Padding(6, 9).Radius(7)
	if row.Hovered() {
		row.Background(a.theme.SurfaceHover)
	}
	row.Children(func() {
		ui.Row(c).Gap(11).Children(func() {
			a.projectIcon(c, project, 54)
			ui.Column(c).Grow(1).Gap(3).Children(func() {
				if chinese := projectChineseName(project.Slug); chinese != "" && chinese != project.Title {
					ui.Row(c).Gap(6).Children(func() {
						ui.Text(c, chinese).Bold().MaxLines(1)
						ui.Text(c, "| "+project.Title).Grow(1).MaxLines(1).TextColor(a.theme.TextMuted).Tooltip(name)
					})
				} else {
					ui.Text(c, name).MaxLines(1).Bold().Tooltip(name)
				}
				description := project.Description
				if tags := project.DisplayCategories; len(tags) > 0 {
					description = strings.Join(tags[:min(3, len(tags))], " · ") + "  " + description
				}
				ui.Text(c, description).MaxLines(1).FontSize(13).TextColor(a.theme.TextMuted).Tooltip(project.Description)
				if details := a.resourceSummary(project); details != "" {
					ui.Text(c, details).MaxLines(1).FontSize(12).TextColor(a.theme.TextMuted)
				}
			})
		})
	})
	if row.Clicked() {
		a.loadProject(project)
	}
}

func (a *app) resourceSummary(project engine.Project) string {
	var info, loaders []string
	for _, category := range project.Categories {
		if slices.ContainsFunc(a.resourceLoaders, func(loader engine.ResourceLoader) bool { return loader.Name == category }) ||
			slices.Contains([]string{"fabric", "forge", "neoforge", "quilt", "iris", "optifine"}, category) {
			loaders = append(loaders, resourceLoaderName(category))
		}
	}
	if len(loaders) > 0 {
		info = append(info, strings.Join(loaders, " / "))
	}
	if len(project.Versions) > 0 {
		versions := slices.Clone(project.Versions[max(0, len(project.Versions)-3):])
		slices.Reverse(versions)
		info = append(info, strings.Join(versions, ", "))
	}
	if project.Downloads > 0 {
		count := fmt.Sprint(project.Downloads)
		switch {
		case project.Downloads >= 100000000:
			count = fmt.Sprintf("%.1f 亿", float64(project.Downloads)/1e8)
		case project.Downloads >= 10000:
			count = fmt.Sprintf("%.1f 万", float64(project.Downloads)/1e4)
		}
		info = append(info, "下载 "+count)
	}
	if !project.DateModified.IsZero() {
		info = append(info, "更新 "+project.DateModified.Format("2006/01/02"))
	}
	return strings.Join(info, " · ")
}

func (a *app) resourceDetails(c *ui.Context) {
	panel(c).Children(func() {
		if ui.Button(c, "返回搜索结果").AlignSelf(ui.Start).Clicked() {
			a.leaveProject()
			a.pageChanged = time.Now()
			return
		}
		ui.Row(c).Gap(12).Children(func() {
			a.projectIcon(c, a.project, 56)
			ui.Column(c).Grow(1).Gap(5).Children(func() {
				ui.Text(c, bilingualName(a.project.Title, a.project.Slug)).FontSize(21).Bold()
				ui.Text(c, a.project.Description).TextColor(a.theme.TextMuted)
			})
		})
	})
	if a.project.ID == "" {
		return
	}
	var games, loaders []string
	seenGames, seenLoaders := map[string]bool{}, map[string]bool{}
	for _, group := range a.resourceGroups {
		if game := group.key.game; !seenGames[game] {
			seenGames[game] = true
			games = append(games, game)
		}
		if loader := group.key.loader; loader != "" && !seenLoaders[loader] {
			seenLoaders[loader] = true
			loaders = append(loaders, loader)
		}
	}
	if !seenGames[a.fileGame] {
		a.fileGame = ""
	}
	if !seenLoaders[a.fileLoader] {
		a.fileLoader = ""
	}
	kind := a.kind()
	var target engine.Instance
	panel(c).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, "游戏版本").FontSize(13)
			resourceFilter(c, "筛选文件的游戏版本", &a.fileGame, games, func(s string) string { return s }, false)
			ui.Text(c, "加载器").FontSize(13)
			resourceFilter(c, "筛选文件的加载器", &a.fileLoader, loaders, resourceLoaderName, false)
		})
		if kind != "modpack" {
			target = a.resourceDestination(c, kind)
		}
	})
	matched := false
	for _, group := range a.resourceGroups {
		if (a.fileGame != "" && group.key.game != a.fileGame) || (a.fileLoader != "" && group.key.loader != a.fileLoader) {
			continue
		}
		matched = true
		panel(c).Key(group.key.game+"/"+group.key.loader).Padding(7, 12).Children(func() {
			expanded := a.openResourceGroups[group.key]
			title := resourceLoaderName(group.key.loader) + " " + group.key.game
			if accordionHeader(c, fmt.Sprintf("%s（%d）", title, len(group.files)), expanded, false) {
				a.openResourceGroups[group.key] = !expanded
			}
			if !expanded {
				return
			}
			ui.Column(c).Key("files").Gap(6).Transition(versionListMotion).Children(func() {
				for _, file := range group.files {
					ui.Divider(c)
					ui.Row(c).Key(file.ID).Gap(8).Children(func() {
						ui.Column(c).Grow(1).Gap(3).Children(func() {
							ui.Text(c, file.Name).Bold().MaxLines(1).Tooltip(file.Name)
							ui.Text(c, file.VersionNumber+" · "+versionTypeName(file.VersionType)).FontSize(12).TextColor(a.theme.TextMuted)
						})
						compatible := kind == "modpack" || (target.Version == group.key.game &&
							(kind == "shader" || target.Loader == group.key.loader) && resourceMatches(kind, target, file) && !a.inUse(target))
						if ui.Button(c, "安装").Label("安装 " + file.Name).Disabled(!compatible || a.busy(downloadJob)).Clicked() {
							a.installResource(file, target)
						}
					})
				}
			})
		})
	}
	if !matched {
		panel(c).Children(func() { ui.Text(c, "没有符合筛选条件的文件。").TextColor(a.theme.TextMuted) })
	}
}

func (a *app) resourceDestination(c *ui.Context, kind string) engine.Instance {
	var compatible []engine.Instance
	for _, instance := range a.instances {
		for _, group := range a.resourceGroups {
			if group.key.game == instance.Version && (kind == "shader" || group.key.loader == instance.Loader && instance.Loader != "") {
				compatible = append(compatible, instance)
				break
			}
		}
	}
	if len(compatible) == 0 {
		ui.Text(c, "没有匹配的本地实例。请先安装对应的游戏版本和加载器。").FontSize(13).TextColor(a.theme.TextMuted)
		return engine.Instance{}
	}
	target := compatible[0]
	for _, i := range compatible {
		if i.ID == a.resourceTarget {
			target = i
			break
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, "安装到").FontSize(13)
		var options []string
		for _, i := range compatible {
			name := i.Name + " · " + i.Version + " · " + loaderName(i)
			if slices.Contains(options, name) {
				name += " (" + i.ID + ")"
			}
			options = append(options, name)
		}
		index := slices.IndexFunc(compatible, func(i engine.Instance) bool { return i.ID == target.ID })
		selected := options[index]
		if ui.Select(c, &selected, options).Label("安装目标实例").Grow(1).Changed() {
			if index := slices.Index(options, selected); index >= 0 {
				target = compatible[index]
				a.resourceTarget = target.ID
			}
		}
	})
	if kind == "shader" {
		ui.Text(c, "请在目标实例中安装文件对应的光影加载器。").FontSize(12).TextColor(a.theme.TextMuted)
	}
	return target
}

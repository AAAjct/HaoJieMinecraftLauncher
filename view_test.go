package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

func TestLongVersionListAndFloatingActions(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page = downloadsPage
	for n := 1; n <= 50; n++ {
		a.versionList = append(a.versionList, engine.Version{ID: fmt.Sprintf("1.21.%d", n), Type: "release"})
	}
	test := ui.NewTester(a.view, 1020, 675)
	test.Frame()
	if test.HasText("显示更多正式版") || test.HasText("从文件安装") {
		t.Fatalf("old download controls still shown: %q", test.Texts())
	}
	if test.HasText("选择 Minecraft 1.21.50") {
		t.Fatal("release group should be collapsed by default")
	}
	if err := test.Click("正式版（50）"); err != nil {
		t.Fatal(err)
	}
	test.Advance(300 * time.Millisecond)
	a.mainScroll.Y = a.mainScroll.MaxY
	test.Frame()
	if !test.HasText("1.21.50") || a.mainScroll.Y == 0 {
		t.Fatalf("last version is not reachable, scroll %+v", a.mainScroll)
	}
	if err := test.Click("回到顶部"); err != nil {
		t.Fatal(err)
	}
	test.Advance(time.Second)
	if a.mainScroll.Y != 0 {
		t.Fatalf("back-to-top left the list at %v", a.mainScroll.Y)
	}
	a.running[&engine.Game{Memory: 2048}] = "first"
	test.Frame()
	stop, hasStop := test.Find("结束所有 Minecraft 实例")
	top, hasTop := test.Find("回到顶部")
	if !hasStop || !hasTop || top.X != stop.X || top.Y >= stop.Y || top.Y < 500 {
		t.Fatalf("running game and long list should stack both actions at the lower right: %v %v", stop, top)
	}
	frame := test.Image()
	x, y := int(stop.X+stop.W/2), int(stop.Y+stop.H/2)
	for _, offset := range [][2]int{{-4, -4}, {4, -4}, {-4, 4}, {4, 4}} {
		if pixel := frame.RGBAAt(x+offset[0], y+offset[1]); pixel.R < 200 || pixel.G < 200 || pixel.B < 200 {
			t.Fatalf("missing diagonal stroke at %v in stop button: %v", offset, pixel)
		}
	}
	status, found := test.Find("就绪")
	middle := status.Y + status.H/2
	if !found || middle < 656 || middle > 658 {
		t.Fatalf("status is not vertically centered in the bottom bar: %v", status)
	}
}

func TestResourceSearchDraftsAndEnter(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page, a.resourceKind = downloadsPage, "模组"
	state := a.resourceSearches["mod"]
	state.loaded, state.total = true, 25
	state.results = []engine.Project{
		{ID: "mouse", Title: "Mouse Tweaks", Slug: "mouse-tweaks"},
		{ID: "other", Title: "Untranslated Project", Slug: "untranslated-project"},
	}
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("Mouse Tweaks") || !test.HasText("Untranslated Project") ||
		!test.HasText("下一页") || test.HasText("模组 · Modrinth") || test.HasText("下载安装") {
		t.Fatalf("unexpected project results: %q", test.Texts())
	}
	for _, step := range []struct{ tab, input, text string }{
		{"模组", "搜索模组", "鼠标手势"},
		{"整合包", "搜索整合包", "2"},
		{"光影", "搜索光影", "3"},
	} {
		if err := test.Click(step.tab); err != nil {
			t.Fatal(err)
		}
		if err := test.Click(step.input); err != nil {
			t.Fatal(err)
		}
		test.Type(step.text)
		test.Key(0, ui.KeyEnter)
		if state := a.resourceSearches[a.kind()]; state.query != step.text || state.submitted != step.text {
			t.Fatalf("%s search draft or Enter submission was lost: %+v", step.tab, state)
		}
	}
	if err := test.Click("模组"); err != nil {
		t.Fatal(err)
	}
	if a.resourceSearches[a.kind()].query != "鼠标手势" || !matchesSearch("建筑师接口", "jianzhushi") ||
		!matchesSearch("预览版", "ylb") {
		t.Fatal("search draft or pinyin matching failed")
	}
}

func TestManageRefreshAndRename(t *testing.T) {
	core := engine.Engine{DataDir: t.TempDir(), GameRoot: t.TempDir()}
	a := newApp(core)
	instance := engine.Instance{ID: "fabric-original", Name: "旧世界", Version: "26.3", Loader: "fabric"}
	folder := core.GameDir(instance)
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "instance.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	test := ui.NewTester(a.view, 830, 565)
	if err := test.Click("实例管理"); err != nil {
		t.Fatal(err)
	}
	test.Advance(time.Second)
	if !test.HasText("26.3 · Fabric") || !test.HasText("打开模组目录") || test.HasText("刷新实例列表") {
		t.Fatalf("instance was not reread on navigation: %q", test.Texts())
	}
	open, hasOpen := test.Find("打开模组目录")
	refresh, hasRefresh := test.Find("刷新模组")
	if !hasOpen || !hasRefresh || open.Y != refresh.Y || open.X+open.W > refresh.X {
		t.Fatalf("refresh must be beside the mods folder button at minimum width: %v %v", open, refresh)
	}
	if err := test.Click("实例名称"); err != nil {
		t.Fatal(err)
	}
	if !test.HasText("保存名称") || test.Focused("保存名称") {
		t.Fatalf("rename input did not open correctly: %q", test.Texts())
	}
	test.Key(ui.Ctrl, ui.KeyA)
	test.Type("新世界")
	test.Key(0, ui.KeyEnter)
	saved, err := core.Instances()
	if err != nil || len(saved) != 1 || saved[0].ID != instance.ID || saved[0].Name != "新世界" ||
		!test.HasText("新世界") {
		t.Fatalf("renaming should persist only the name, got %+v: %v; %q", saved, err, test.Texts())
	}
	if err := os.Mkdir(filepath.Join(folder, "mods"), 0700); err != nil {
		t.Fatal(err)
	}
	modPath := filepath.Join(folder, "mods", "example.jar.disabled")
	if err := os.WriteFile(modPath, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	if test.HasText("example.jar") {
		t.Fatal("mod should not appear before refreshing")
	}
	if err := test.Click("刷新模组"); err != nil {
		t.Fatal(err)
	}
	if len(a.mods) != 1 || a.mods[0].Filename != "example.jar.disabled" {
		t.Fatalf("refresh did not reread the mod directory: %+v", a.mods)
	}
	if err := test.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(modPath); !os.IsNotExist(err) {
		t.Fatalf("模组应立即删除: %v", err)
	}
	if err := test.Click("删除实例"); err != nil {
		t.Fatal(err)
	}
	if err := test.Click("确认删除实例"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("实例目录应已删除: %v", err)
	}
}

func TestLocalModRowsDisplayMetadata(t *testing.T) {
	core := engine.Engine{DataDir: t.TempDir()}
	instance := engine.Instance{ID: "fabric", Name: "测试实例", Loader: "fabric"}
	dir := filepath.Join(core.GameDir(instance), "mods")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "demo-1.2.jar"))
	if err != nil {
		t.Fatal(err)
	}
	jar := zip.NewWriter(file)
	for name, data := range map[string][]byte{
		"fabric.mod.json": []byte(`{"id":"demo","name":"Demo Mod","version":"1.2","description":"A useful library.","icon":{"64":"icon.png"},"custom":{"modmenu":{"badges":["library"]}}}`),
		"icon.png":        stevePNG,
	} {
		entry, err := jar.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := jar.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core.DataDir, "instances", instance.ID, "instance.json"),
		[]byte(`{"ID":"fabric","Name":"测试实例","Loader":"fabric"}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := newApp(core)
	if a.current().ID != instance.ID || len(a.mods) != 0 || len(a.modIcons) != 0 {
		t.Fatalf("startup should select the instance without opening its mods: %+v", a.mods)
	}
	a.navigate(managePage)
	test := ui.NewTester(a.view, 830, 565)
	icon, hasIcon := test.Find("Demo Mod 图标")
	title, hasTitle := test.Find("Demo Mod")
	version, hasVersion := test.Find("|  1.2")
	details, hasDetails := test.Find("支持库  demo-1.2: A useful library.")
	if !hasIcon || !hasTitle || !hasVersion || !hasDetails || a.modIcons["demo-1.2.jar"] == nil ||
		icon.X+icon.W > title.X || title.X >= version.X || title.X != details.X || details.Y <= title.Y {
		t.Fatalf("unexpected mod row layout: %v %v %v %v", icon, title, version, details)
	}
	a.navigate(launchPage)
	if len(a.mods) != 0 || len(a.modIcons) != 0 {
		t.Fatal("leaving instance management should release the mod metadata and icons")
	}
}

func TestResourceDetailsGroupAndFilterFiles(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page, a.resourceKind = downloadsPage, "模组"
	a.instances = []engine.Instance{{ID: "fabric", Name: "Fabric 实例", Version: "1.21.1", Loader: "fabric"}}
	a.project = engine.Project{ID: "mouse", Title: "Mouse Tweaks"}
	a.resourceGroups = groupResourceVersions([]engine.ResourceVersion{
		{ID: "modern", Name: "Mouse Tweaks Modern", VersionNumber: "2.0", GameVersions: []string{"1.21.1"}, Loaders: []string{"fabric", "forge"}},
		{ID: "older", Name: "Mouse Tweaks Old", VersionNumber: "1.0", GameVersions: []string{"1.20.1"}, Loaders: []string{"forge"}},
		{ID: "quilt", Name: "Unsupported", GameVersions: []string{"1.21.1"}, Loaders: []string{"quilt"}},
	}, nil, "mod")
	if slices.ContainsFunc(a.resourceGroups, func(group resourceGroup) bool { return group.key.loader == "quilt" }) {
		t.Fatal("Quilt should not appear as a resource download")
	}
	test := ui.NewTester(a.view, 1020, 675)
	if test.HasText("Mouse Tweaks Modern") || !test.HasText("Forge 1.20.1（1）") {
		t.Fatalf("expected collapsed file groups: %q", test.Texts())
	}
	if err := test.Click("Fabric 1.21.1（1）"); err != nil {
		t.Fatal(err)
	}
	test.Advance(300 * time.Millisecond)
	if !test.HasText("Mouse Tweaks Modern") {
		t.Fatalf("expanding the group did not show its file: %q", test.Texts())
	}
	if err := test.Click("筛选文件的加载器"); err != nil {
		t.Fatal(err)
	}
	if err := test.Click("Fabric"); err != nil {
		t.Fatal(err)
	}
	if a.fileLoader != "fabric" || test.HasText("Forge 1.20.1（1）") {
		t.Fatalf("loader filter did not hide incompatible groups: %q", test.Texts())
	}
}

func TestResourceRowsKeepIconsOnLeft(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page, a.resourceKind = downloadsPage, "模组"
	state := a.resourceSearches["mod"]
	state.loaded = true
	state.results = []engine.Project{
		{ID: "short", Title: "Short", Description: "One"},
		{ID: "long", Title: "Long", Description: "A long description that would otherwise make this row substantially wider than the other project row"},
	}
	test := ui.NewTester(a.view, 830, 565)
	short, foundShort := test.Find("Short 图标")
	long, foundLong := test.Find("Long 图标")
	if !foundShort || !foundLong || short.X != long.X || short.W != 54 || long.W != 54 {
		t.Fatalf("icons must have a common left edge and width: %v %v", short, long)
	}
}

func TestInstallTargetsMatchFileGroup(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	fabric := engine.Instance{ID: "fabric", Version: "1.21.1", Loader: "fabric"}
	forge := engine.Instance{ID: "forge", Version: "1.20.1", Loader: "forge"}
	a.instances = []engine.Instance{fabric, forge}
	file := engine.ResourceVersion{GameVersions: []string{forge.Version}, Loaders: []string{forge.Loader}}
	group := loaderKey{forge.Version, forge.Loader}
	if dest := a.fileDestination("mod", group, file, fabric); dest.ID != forge.ID {
		t.Fatalf("compatible Forge file should be installable from a Fabric selection: %+v", dest)
	}
	a.running[&engine.Game{Memory: 2048}] = forge.ID
	if dest := a.fileDestination("mod", group, file, fabric); dest.ID != "" {
		t.Fatalf("should not install into a running instance: %+v", dest)
	}
	file.GameVersions = append(file.GameVersions, fabric.Version)
	file.Loaders = append(file.Loaders, fabric.Loader)
	if dest := a.fileDestination("mod", group, file, fabric); dest.ID != fabric.ID {
		t.Fatalf("a shared file should still install into the compatible selection: %+v", dest)
	}
}

func TestResourceDetailsRecommendsSelectedInstance(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page, a.resourceKind = downloadsPage, "模组"
	a.project = engine.Project{ID: "example", Title: "Example"}
	forge := engine.Instance{ID: "forge", Name: "旧存档", Version: "1.20.1", Loader: "forge"}
	fabric := engine.Instance{ID: "fabric", Name: "生存世界", Version: "1.21.1", Loader: "fabric"}
	a.instances = []engine.Instance{forge, fabric}
	a.resourceTarget = fabric.ID
	a.resourceGroups = []resourceGroup{
		{key: loaderKey{forge.Version, forge.Loader}, files: []engine.ResourceVersion{{ID: "forge"}}},
		{key: loaderKey{fabric.Version, fabric.Loader}, files: []engine.ResourceVersion{{ID: "fabric"}}},
	}
	test := ui.NewTester(a.view, 1020, 675)
	first, firstFound := test.Find("Fabric 1.21.1（1）")
	second, secondFound := test.Find("Forge 1.20.1（1）")
	if !firstFound || !secondFound || first.Y >= second.Y || !test.HasText(fabric.Name) ||
		test.HasText(fabric.Name+" · 1.21.1 · Fabric") {
		t.Fatalf("expected name only and selected group first: %q", test.Texts())
	}
	if err := test.Click("安装目标实例"); err != nil {
		t.Fatal(err)
	}
	if err := test.Click(forge.Name); err != nil {
		t.Fatal(err)
	}
	first, firstFound = test.Find("Forge 1.20.1（1）")
	second, secondFound = test.Find("Fabric 1.21.1（1）")
	if !firstFound || !secondFound || first.Y >= second.Y || a.resourceTarget != forge.ID {
		t.Fatalf("changing the target should reorder the groups: %q", test.Texts())
	}
}

func TestMainNavigation(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.versionList = []engine.Version{{ID: "1.21.1", Type: "release"}}
	a.jobs = []*job{{kind: downloadJob, title: "下载", message: "下载资源 · 5/10 · 1.4 MiB/s"}}
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("离线玩家") || test.HasText("HaoJieMinecraftLauncher") || test.HasText("设置") {
		t.Fatalf("home content: %q", test.Texts())
	}
	for _, step := range []struct{ button, text string }{
		{"下载", "Minecraft 版本"},
		{"实例管理", "安装实例后可在此管理版本。"},
		{"任务", "任务管理"},
		{"启动", "离线玩家"},
	} {
		if err := test.Click(step.button); err != nil {
			t.Fatalf("%s: %v", step.button, err)
		}
		if !test.HasText(step.text) {
			t.Fatalf("%s: missing %q in %q", step.button, step.text, test.Texts())
		}
	}
}

func TestDownloadCategoriesAndLocalImportLayout(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page = downloadsPage
	test := ui.NewTester(a.view, 830, 565)
	test.Frame()
	if test.HasText("世界") {
		t.Fatal("world downloads should not appear in the download sidebar")
	}
	for _, entry := range []struct{ name, field string }{
		{"整合包", "从文件安装"}, {"数据包", "搜索数据包"}, {"资源包", "搜索资源包"},
	} {
		if err := test.Click(entry.name); err != nil {
			t.Fatal(err)
		}
		test.Advance(300 * time.Millisecond)
		if _, ok := test.Find(entry.field); !ok {
			t.Fatalf("download category %s did not expose %s", entry.name, entry.field)
		}
		if _, ok := test.Find("资源来源"); !ok {
			t.Fatalf("download category %s did not expose the source picker", entry.name)
		}
	}
	before, ok := test.Find("就绪")
	if !ok {
		t.Fatal("missing status bar")
	}
	a.jobs = []*job{{kind: importJob, title: "从文件安装", message: "正在安装模组"}}
	test.Frame()
	after, ok := test.Find("就绪")
	if !ok || before != after || test.HasText("取消导入") {
		t.Fatalf("local mod import moved the status bar: %v -> %v", before, after)
	}
}

func TestParallelDownloadJobsKeepSeparateTargets(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.jobs = []*job{
		{kind: downloadJob, key: "mod-one", title: "模组一", target: "fabric", cancel: func() {}},
		{kind: downloadJob, key: "mod-two", title: "模组二", target: "fabric", cancel: func() {}},
	}
	if a.downloadCount() != 2 || !a.downloading("mod-one", "fabric") || a.downloading("mod-three", "fabric") ||
		!a.inUse(engine.Instance{ID: "fabric"}) || a.gameInUse(engine.Instance{ID: "fabric"}) {
		t.Fatal("downloads must block launching and edits without blocking other downloads")
	}
	a.page = tasksPage
	test := ui.NewTester(a.view, 1020, 675)
	if err := test.Click("取消 模组一"); err != nil {
		t.Fatal(err)
	}
	if !a.jobs[0].cancelling || a.jobs[1].cancelling {
		t.Fatal("cancelling one download affected another")
	}
}

func TestPageTransitionAnimatesSidebarWidth(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	test := ui.NewTester(a.view, 1020, 675)
	test.Frame()
	background := test.Image().RGBAAt(300, 550)
	sidebarEdge := func() int {
		frame := test.Image()
		for x := 200; x < 300; x++ {
			if frame.RGBAAt(x, 550) == background {
				return x
			}
		}
		return 300
	}
	if edge := sidebarEdge(); edge != 278 {
		t.Fatalf("starting sidebar width = %d", edge)
	}
	if err := test.Click("任务"); err != nil {
		t.Fatal(err)
	}
	test.Advance(110 * time.Millisecond)
	if edge := sidebarEdge(); edge <= 218 || edge >= 278 {
		t.Fatalf("sidebar width should interpolate between pages, got %d", edge)
	}
	test.Advance(300 * time.Millisecond)
	if edge := sidebarEdge(); edge != 218 || !test.HasText("任务管理") {
		t.Fatalf("task page did not settle, sidebar width = %d", edge)
	}
}

func TestLaunchPageDirectNicknameAndInstance(t *testing.T) {
	core := engine.Engine{DataDir: t.TempDir()}
	a := newApp(core)
	a.nickname, a.settings.Nickname = "", ""
	a.selected = "fabric"
	a.instances = []engine.Instance{{ID: "fabric", Name: "生存世界", Version: "1.21.1", Loader: "fabric"}}
	test := ui.NewTester(a.view, 1020, 675)
	if steveAvatar == nil || !test.HasText("生存世界") || test.HasText("生存世界（1.21.1）") {
		t.Fatalf("unexpected launch page: %q", test.Texts())
	}
	if err := test.Click("离线玩家名"); err != nil {
		t.Fatal(err)
	}
	test.Type("Steve_New")
	if got := core.LoadSettings().Nickname; got != "Steve_New" {
		t.Fatalf("direct nickname edit did not persist: %q", got)
	}
}

func TestAvatarContextMenuSavesSkinAndShowsRunningCount(t *testing.T) {
	core := engine.Engine{DataDir: t.TempDir()}
	a := newApp(core)
	a.selected = "fabric"
	a.instances = []engine.Instance{{ID: "fabric", Name: "生存世界"}}
	first, second := &engine.Game{Memory: 2048}, &engine.Game{Memory: 2048}
	a.running[first], a.running[second] = "fabric", "fabric"
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("再次启动（运行中 2）") || a.installInProgress(a.current()) {
		t.Fatal("running games should be counted separately and allow another launch")
	}
	delete(a.running, first)
	test.Frame()
	if !test.HasText("再次启动（运行中 1）") || !a.gameInUse(a.current()) {
		t.Fatal("one game's exit should leave the other running")
	}
	if err := test.RightClick("史蒂夫头像"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"史蒂夫", "艾利克斯", "本地皮肤"} {
		if !slices.Contains(test.Menu(), item) {
			t.Fatalf("missing %s in avatar menu: %v", item, test.Menu())
		}
	}
	if err := test.ChooseMenuItem("艾利克斯"); err != nil {
		t.Fatal(err)
	}
	if core.LoadSettings().Skin != engine.AlexSkin {
		t.Fatal("avatar menu did not save the selected skin")
	}
	test.Frame()
	avatar, found := test.Find("艾利克斯头像")
	if !found {
		t.Fatal("blank avatar should still be available for right-click")
	}
	frame := test.Image()
	x, y := int(avatar.X), int(avatar.Y)
	background := frame.RGBAAt(x, y)
	for dy := range int(avatar.H) {
		for dx := range int(avatar.W) {
			if frame.RGBAAt(x+dx, y+dy) != background {
				t.Fatal("an unloaded avatar should be visually blank")
			}
		}
	}
	var skin bytes.Buffer
	if err := png.Encode(&skin, image.NewNRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	next := a.settings
	next.Skin, next.SkinPNG, next.SkinFilename = engine.LocalSkin, skin.Bytes(), "自制皮肤.v2.PNG"
	a.saveSkin(next)
	a = newApp(core)
	test = ui.NewTester(a.view, 1020, 675)
	if err := test.RightClick("本地皮肤头像"); err != nil {
		t.Fatal(err)
	}
	if items := test.Menu(); !slices.Contains(items, "自制皮肤.v2") || slices.Contains(items, "自制皮肤.v2.PNG") {
		t.Fatalf("imported filename should appear without the extension after restart: %v", items)
	}
}

func TestChooseGameAndLoaderBuild(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.versionList = []engine.Version{
		{ID: "1.21.1", Type: "release"}, {ID: "1.20.1", Type: "release"},
		{ID: "26.4-snapshot-10", Type: "snapshot"}, {ID: "b1.7.3", Type: "old_beta"},
	}
	a.loaderLists[loaderKey{"1.20.1", "forge"}] = []engine.LoaderBuild{
		{Version: "47.4.20", Stable: true, Recommended: true}, {Version: "47.1.0", Stable: true},
	}
	a.loaderLists[loaderKey{"1.20.1", "fabric"}] = []engine.LoaderBuild{{Version: "0.18.1", Stable: true}}
	a.page = downloadsPage
	test := ui.NewTester(a.view, 1020, 675)
	for _, label := range []string{"正式版（2）", "选择 Minecraft 1.20.1", "Forge（2）", "选择 Forge 47.1.0", "Fabric（1）", "选择 Fabric 0.18.1"} {
		if err := test.Click(label); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		test.Advance(250 * time.Millisecond)
		if label == "选择 Forge 47.1.0" {
			i := a.installSelection()
			if i.Version != "1.20.1" || i.Loader != "forge" || i.Build != "47.1.0" {
				t.Fatalf("unexpected Forge install selection %+v", i)
			}
		}
	}
	if i := a.installSelection(); i.Version != "1.20.1" || i.Loader != "fabric" || i.Build != "0.18.1" {
		t.Fatalf("unexpected Fabric install selection %+v", i)
	}
	if err := test.Click("返回版本列表"); err != nil {
		t.Fatal(err)
	}
	if i := a.installSelection(); i.Version != "" || i.Loader != "" || i.Build != "" {
		t.Fatalf("stale install selection %+v", i)
	}
}

func TestInstanceImportAvailableWithoutAnInstance(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.page = managePage
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("我的实例") || !test.HasText("导入") || test.HasText("从文件安装") {
		t.Fatalf("unexpected empty instance controls: %v", test.Texts())
	}
	files := splitImportFiles([]string{"first.jar", "second.JAR", "first.zip", "second.RAR", "third.mrpack", "other.txt"})
	if len(files.mods) != 2 || len(files.packs) != 3 {
		t.Fatalf("incorrect file classification: %+v", files)
	}
}

func TestDropPinsSelectedInstance(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	first := engine.Instance{ID: "first", Name: "Fabric 测试", Version: "1.21.1"}
	second := engine.Instance{ID: "second", Name: "第二个实例", Version: "1.21.4"}
	a.instances = []engine.Instance{first, second}
	a.selected = first.ID
	a.receiveFiles([]string{"D:/downloads/Sodium.jar"})
	if !a.importConfirm || a.pendingImport.instance.ID != first.ID {
		t.Fatal("drop did not require confirmation for the selected instance")
	}
	a.selected = second.ID
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("是否将 1 个模组安装至 Fabric 测试（Minecraft 1.21.1）？") {
		t.Fatalf("confirmation did not name the original target: %q", test.Texts())
	}
	if test.Focused("安装模组") || test.Focused("取消") {
		t.Fatal("confirmation should open without focusing a button")
	}
	if err := test.Click("取消"); err != nil || a.importConfirm {
		t.Fatalf("cancel did not dismiss the modal: %v", err)
	}
}

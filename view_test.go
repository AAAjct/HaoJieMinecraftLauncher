package main

import (
	"fmt"
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
	if test.HasText("显示更多正式版") || test.HasText("从文件安装") || test.HasText("资源包") {
		t.Fatalf("old download controls still shown: %q", test.Texts())
	}
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
	a.running["first"] = &engine.Game{}
	test.Frame()
	stop, hasStop := test.Find("结束所有 Minecraft 实例")
	top, hasTop := test.Find("回到顶部")
	if !hasStop || !hasTop || top.X != stop.X || top.Y <= stop.Y || top.Y > 150 {
		t.Fatalf("running game and long list should stack both actions at the upper right: %v %v", stop, top)
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
	}, nil)
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

func TestMainNavigation(t *testing.T) {
	a := newApp(engine.Engine{DataDir: t.TempDir()})
	a.versionList = []engine.Version{{ID: "1.21.1", Type: "release"}}
	a.jobs[downloadJob] = &job{message: "下载资源 · 5/10 · 1.4 MiB/s"}
	test := ui.NewTester(a.view, 1020, 675)
	if !test.HasText("离线玩家") || test.HasText("HaoJieMinecraftLauncher") || test.HasText("设置") {
		t.Fatalf("home content: %q", test.Texts())
	}
	for _, step := range []struct{ button, text string }{
		{"下载", "Minecraft 版本"},
		{"实例管理", "安装实例后可管理模组和查看日志。"},
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
	for _, label := range []string{"选择 Minecraft 1.20.1", "Forge（2）", "选择 Forge 47.1.0", "Fabric（1）", "选择 Fabric 0.18.1"} {
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

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
)

const (
	launchPage    = "启动"
	downloadsPage = "下载"
	managePage    = "实例管理"
	tasksPage     = "任务"
)

type app struct {
	engine                engine.Engine
	window                *mygo.Window
	theme                 *ui.Theme
	page                  string
	settings              engine.Settings
	nickname              string
	instances             []engine.Instance
	selected              string
	mods                  []engine.Mod
	DeleteTarget          string
	modFilter             string
	renaming, renameValue string
	running               map[string]*engine.Game
	stopLaunch            bool

	versionList                                []engine.Version
	versionStamp                               time.Time
	versionLoading                             bool
	catalogContext                             context.Context
	versionFilter, targetVersion, instanceName string
	versionGroups                              map[string]bool
	openLoaders                                map[string]bool
	loaderLists                                map[loaderKey][]engine.LoaderBuild
	buildStamps                                map[loaderKey]time.Time
	buildLoading                               map[loaderKey]bool
	buildErrors                                map[loaderKey]string
	selectedLoader, selectedBuild              string
	resourceKind                               string
	resourceSearches                           map[string]*resourceSearch
	activeSearch                               *resourceSearch
	project                                    engine.Project
	resourceGroups                             []resourceGroup
	openResourceGroups                         map[loaderKey]bool
	fileGame, fileLoader, resourceTarget       string
	resourceLoaders                            []engine.ResourceLoader
	projectIcons                               map[string]ui.ImageSource
	mainScroll, instanceScroll                 ui.ScrollState

	status                       string
	jobs                         map[jobKind]*job
	closing                      bool
	downloadTarget, launchTarget string
	pageChanged                  time.Time
	importConfirm                bool
	pendingImport                importRequest
}

type operation func(context.Context, func(string)) (func(), error)

type jobKind string

const (
	downloadJob jobKind = "下载"
	catalogJob  jobKind = "检索"
	launchJob   jobKind = "启动"
)

type job struct {
	message    string
	cancel     context.CancelFunc
	cancelling bool
}

func (a *app) busy(kind jobKind) bool { return a.jobs[kind] != nil }

func (a *app) inUse(i engine.Instance) bool {
	return i.ID != "" && (a.running[i.ID] != nil || a.launchTarget == i.ID || a.downloadTarget == i.ID)
}

func (a *app) navigate(page string) {
	if a.page != page {
		a.page, a.pageChanged = page, time.Now()
		a.mainScroll.Y = 0
		a.renaming = ""
		if page == managePage {
			a.refreshInstances()
		}
	}
}

func (a *app) openVersions() {
	a.navigate(downloadsPage)
	a.resourceKind = "游戏版本"
	a.leaveProject()
	a.downloadVersions(false)
}

func newApp(core engine.Engine) *app {
	theme := ui.DarkTheme()
	theme.Background = ui.Hex("#101825")
	theme.Text = ui.Hex("#E8F1FF")
	theme.TextMuted = ui.Hex("#9FB1C9")
	theme.Surface = ui.Hex("#273344")
	theme.SurfaceHover = ui.Hex("#344C65")
	theme.SurfacePressed = theme.SurfaceHover
	theme.Border = ui.Hex("#384C65")
	theme.Accent = ui.Hex("#2F79CD")
	theme.AccentHover = ui.Hex("#4F98E4")
	theme.AccentPressed = theme.AccentHover
	theme.InactiveAccent = theme.Accent
	theme.AccentText = ui.Hex("#FFFFFF")
	s := core.LoadSettings()
	a := &app{
		engine: core, theme: theme, page: launchPage, settings: s,
		nickname: s.Nickname, selected: s.Instance,
		running: map[string]*engine.Game{}, resourceKind: "游戏版本",
		status: "就绪", jobs: map[jobKind]*job{},
		versionGroups: map[string]bool{"release": true},
		openLoaders:   map[string]bool{}, loaderLists: map[loaderKey][]engine.LoaderBuild{},
		buildStamps: map[loaderKey]time.Time{}, buildLoading: map[loaderKey]bool{}, buildErrors: map[loaderKey]string{},
		projectIcons: map[string]ui.ImageSource{}, openResourceGroups: map[loaderKey]bool{},
		resourceSearches: map[string]*resourceSearch{"mod": {}, "modpack": {}, "shader": {}},
	}
	a.versionList, a.versionStamp = core.CachedVersions()
	a.refreshInstances()
	return a
}

func (a *app) current() engine.Instance {
	for _, i := range a.instances {
		if i.ID == a.selected {
			return i
		}
	}
	return engine.Instance{}
}

func (a *app) refreshInstances() {
	var err error
	a.instances, err = a.engine.Instances()
	if err != nil {
		a.status = err.Error()
	}
	if a.current().ID == "" {
		a.selected, a.renaming = "", ""
		if len(a.instances) > 0 {
			a.selected = a.instances[0].ID
		}
	}
	a.refreshMods()
}

func (a *app) refreshMods() {
	a.mods = nil
	if i := a.current(); i.ID != "" {
		var err error
		a.mods, err = a.engine.Mods(i)
		if err != nil {
			a.status = err.Error()
		}
	}
}

func (a *app) selectInstance(i engine.Instance) {
	if a.selected == i.ID && a.settings.Instance == i.ID {
		return
	}
	a.selected = i.ID
	a.renaming = ""
	a.settings.Instance = i.ID
	if err := a.engine.SaveSettings(a.settings); err != nil {
		a.status = err.Error()
	}
	a.DeleteTarget = ""
	a.modFilter = ""
	a.refreshMods()
	a.preloadBuilds(i.Version)
}

func (a *app) installed(i engine.Instance) {
	hadInstance := a.current().ID != ""
	a.refreshInstances()
	if !hadInstance {
		a.selectInstance(i)
	}
	a.status = "已安装 " + i.Name
}

func (a *app) start(kind jobKind, message string, work operation) {
	if a.busy(kind) || a.closing {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	task := &job{message: message, cancel: cancel}
	a.jobs[kind] = task
	go func() {
		defer cancel()
		finish, err := work(ctx, func(s string) {
			a.window.Update(func() {
				if a.jobs[kind] == task && !task.cancelling {
					task.message = s
				}
			})
		})
		a.window.Update(func() {
			delete(a.jobs, kind)
			switch kind {
			case catalogJob:
				if a.activeSearch != nil {
					a.activeSearch.failed = err != nil
					a.activeSearch = nil
				}
			case downloadJob:
				a.downloadTarget = ""
			case launchJob:
				a.launchTarget = ""
			}
			if a.closing {
				if kind == launchJob && err == nil && finish != nil {
					finish()
				}
				if len(a.jobs) == 0 {
					a.window.Close()
				}
				return
			}
			if err != nil {
				if kind == downloadJob {
					a.refreshInstances()
				}
				a.status = err.Error()
				if task.cancelling {
					a.status = string(kind) + "已取消。"
				}
			} else if finish != nil {
				finish()
			}
		})
	}()
}

func (a *app) install() {
	i := a.installSelection()
	a.start(downloadJob, "正在准备安装 Minecraft…", func(ctx context.Context, status func(string)) (func(), error) {
		i, err := a.engine.Install(ctx, i, status)
		return func() { a.installed(i) }, err
	})
}

func (a *app) play(i engine.Instance) {
	if a.inUse(i) || a.busy(launchJob) {
		return
	}
	account, err := engine.LocalAccount(strings.TrimSpace(a.nickname))
	if err != nil {
		a.status = err.Error()
		return
	}
	reserved := 0
	for _, game := range a.running {
		reserved += game.Memory
	}
	a.stopLaunch = false
	a.launchTarget = i.ID
	a.start(launchJob, "正在准备启动…", func(ctx context.Context, status func(string)) (func(), error) {
		memory, err := engine.AutomaticMemory(i.Loader != "", reserved)
		if err != nil {
			return nil, err
		}
		game, err := a.engine.Launch(ctx, i, account, memory, status)
		return func() {
			if a.closing {
				_ = game.Stop()
				return
			}
			a.running[i.ID] = game
			a.status = fmt.Sprintf("Minecraft 已启动 · %s · 分配 %d MiB", i.Name, memory)
			if a.stopLaunch {
				a.stopGames()
			}
			go func() {
				err := <-game.Done
				a.window.Update(func() {
					delete(a.running, i.ID)
					if !a.busy(launchJob) {
						a.status = "游戏已退出 · " + i.Name
						if game.Stopping {
							a.status = "已结束 Minecraft · " + i.Name
						} else if err != nil {
							a.status = "游戏异常退出，可在实例管理页查看启动日志。"
						}
					}
				})
			}()
		}, err
	})
}

func (a *app) stopGames() {
	if task := a.jobs[launchJob]; task != nil {
		a.stopLaunch = true
		task.cancelling = true
		task.message = "正在取消启动…"
		task.cancel()
	}
	a.status = "正在结束 Minecraft 实例…"
	for _, game := range a.running {
		if !game.Stopping {
			if err := game.Stop(); err != nil {
				a.status = "结束 Minecraft 失败：" + err.Error()
			}
		}
	}
}

func (a *app) kind() string {
	switch a.resourceKind {
	case "模组":
		return "mod"
	case "整合包":
		return "modpack"
	case "光影":
		return "shader"
	default:
		return ""
	}
}

func (a *app) leaveProject() {
	a.resourceGroups = nil
	a.project = engine.Project{}
	a.mainScroll.Y = 0
}

type resourceSearch struct {
	query, submitted, game, loader string
	results                        []engine.Project
	page, total, requested         int
	loaded, failed                 bool
}

func (a *app) search(page int) {
	kind := a.kind()
	if kind == "" || a.busy(catalogJob) || a.window == nil || a.closing {
		return
	}
	state := a.resourceSearches[kind]
	state.requested, state.failed = page, false
	a.activeSearch = state
	query := searchName(state.submitted)
	filter := engine.Instance{Version: state.game, Loader: state.loader}
	a.start(catalogJob, "正在搜索 Modrinth…", func(ctx context.Context, _ func(string)) (func(), error) {
		hits, total, err := engine.Search(ctx, kind, query, filter, page)
		return func() {
			state.results, state.total, state.page, state.loaded = hits, total, page, true
			if a.page == downloadsPage && a.kind() == kind {
				a.mainScroll.Y = 0
			}
			a.fetchProjectIcons(hits)
			a.status = "搜索完成。"
		}, err
	})
}

func (a *app) submitResourceSearch(state *resourceSearch) {
	state.submitted = strings.TrimSpace(state.query)
	state.results, state.page, state.total, state.loaded = nil, 0, 0, false
	a.search(0)
}

func (a *app) loadProject(p engine.Project) {
	kind := a.kind()
	state := a.resourceSearches[kind]
	a.start(catalogJob, "正在读取项目版本…", func(ctx context.Context, _ func(string)) (func(), error) {
		list, err := engine.ResourceVersions(ctx, p.ID, kind, engine.Instance{})
		return func() {
			if a.page != downloadsPage || a.kind() != kind {
				return
			}
			a.project, a.resourceGroups = p, groupResourceVersions(list, a.versionList)
			a.fileGame, a.fileLoader = state.game, state.loader
			a.resourceTarget = a.selected
			clear(a.openResourceGroups)
			a.pageChanged = time.Now()
			a.mainScroll.Y = 0
			a.status = "项目版本已加载。"
		}, err
	})
}

func (a *app) installResource(v engine.ResourceVersion, i engine.Instance) {
	kind := a.kind()
	if a.busy(downloadJob) || (kind != "modpack" && (a.inUse(i) || !resourceMatches(kind, i, v))) {
		return
	}
	if kind != "modpack" {
		a.downloadTarget = i.ID
	}
	a.start(downloadJob, "正在准备下载…", func(ctx context.Context, status func(string)) (func(), error) {
		if kind == "modpack" {
			pack, err := a.engine.DownloadPack(ctx, v, status)
			return func() { a.installed(pack) }, err
		}
		err := a.engine.InstallResource(ctx, kind, i, v, status)
		return func() {
			a.refreshMods()
			a.status = "已安装 " + v.Name
		}, err
	})
}

func (a *app) saveNickname() {
	name := strings.TrimSpace(a.nickname)
	if _, err := engine.LocalAccount(name); err != nil {
		a.status = err.Error()
		return
	}
	if name == a.settings.Nickname {
		return
	}
	next := a.settings
	next.Nickname = name
	if err := a.engine.SaveSettings(next); err != nil {
		a.status = err.Error()
		return
	}
	a.settings = next
	a.status = "离线玩家名已保存。"
}
func (a *app) openFile(path string) {
	if err := mygo.Shell.OpenPath(path); err != nil {
		a.status = fmt.Sprintf("无法打开 %s。", filepath.Base(path))
	}
}

func loaderName(i engine.Instance) string {
	switch i.Loader {
	case "fabric":
		return "Fabric"
	case "quilt":
		return "Quilt"
	case "forge":
		return "Forge"
	default:
		return "原版"
	}
}

func (a *app) hasVersion() bool {
	return slices.ContainsFunc(a.versionList, func(v engine.Version) bool { return v.ID == a.targetVersion })
}

func main() {
	data, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	gameRoot := filepath.Join(filepath.Dir(exe), ".minecraft")
	a := newApp(engine.Engine{DataDir: filepath.Join(data, "HJML"), GameRoot: gameRoot})
	catalogCtx, stopCatalog := context.WithCancel(context.Background())
	defer stopCatalog()
	a.catalogContext = catalogCtx
	if err := os.MkdirAll(gameRoot, 0700); err != nil {
		a.status = "无法创建 .minecraft：" + err.Error()
	}
	mygo.App.WhenReady(func() {
		a.window = mygo.NewWindow(mygo.WindowOptions{
			Title: "HaoJieMinecraftLauncher", Width: 1020, Height: 675,
			MinWidth: 830, MinHeight: 565, Frameless: true, CornerRadius: 8, Content: ui.View(a.view),
		})
		removeWindowBorder(a.window)
		a.window.OnClosed(stopCatalog)
		a.downloadVersions(false)
		a.preloadRecentBuilds()
		go func() {
			loaders, err := engine.ResourceLoaders(catalogCtx)
			if err == nil && catalogCtx.Err() == nil {
				a.window.Update(func() { a.resourceLoaders = loaders })
			}
		}()
		a.window.OnFileDrop(func(event *mygo.FileDropEvent) {
			paths := slices.Clone(event.Paths)
			a.window.Update(func() { a.receiveFiles(paths) })
		})
		a.window.OnClose(func(e *mygo.CloseEvent) {
			if len(a.jobs) > 0 {
				e.PreventDefault()
				a.closing = true
				for _, task := range a.jobs {
					task.cancelling = true
					task.cancel()
				}
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"hjml/internal/engine"
	_ "hjml/locales"
)

const (
	launchPage    = "启动"
	downloadsPage = "下载"
	managePage    = "实例管理"
	tasksPage     = "任务"
)

type app struct {
	engine        engine.Engine
	window        *mygo.Window
	theme         *ui.Theme
	page          string
	settings      engine.Settings
	nickname      string
	instances     []engine.Instance
	selected      string
	mods          []engine.Mod
	modIcons      map[string]ui.ImageSource
	deleteConfirm bool
	modFilter     string
	renameValue   string
	running       map[*engine.Game]string
	stopLaunch    bool
	skinAvatar    ui.ImageSource
	alexLoading   bool

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
	modNames                                   *engine.ModNames
	loadModNames                               func() *engine.ModNames
	modNamesLoading                            bool
	project                                    engine.Project
	projectVersions                            map[string]projectVersionResult
	resourceGroups                             []resourceGroup
	openResourceGroups                         map[loaderKey]bool
	fileGame, fileLoader, resourceTarget       string
	projectPage                                int
	projectMore                                bool
	datapackWorld, datapackInstance            string
	datapackWorlds                             []string
	projectIcons                               map[string]ui.ImageSource
	cancelProjectIcons                         context.CancelFunc
	mainScroll, instanceScroll                 ui.ScrollState

	status        string
	jobs          []*job
	closing       bool
	launchTarget  string
	importConfirm bool
	pendingImport importRequest
}

type operation func(context.Context, func(string)) (func(), error)

type jobKind string

const (
	downloadJob jobKind = "下载"
	catalogJob  jobKind = "检索"
	launchJob   jobKind = "启动"
	importJob   jobKind = "导入"
)

type job struct {
	kind       jobKind
	key, title string
	target     string
	message    string
	cancel     context.CancelFunc
	onDone     func(error)
	cancelling bool
	superseded bool
}

func (task *job) requestCancel() {
	task.cancelling = true
	task.message = "正在取消并清理临时文件…"
	task.cancel()
}

func (a *app) downloadCount() int {
	count := 0
	for _, task := range a.jobs {
		if task.kind == downloadJob {
			count++
		}
	}
	return count
}

func (a *app) activeJob(kind jobKind) *job {
	for _, task := range a.jobs {
		if task.kind == kind {
			return task
		}
	}
	return nil
}

func (a *app) busy(kind jobKind) bool { return a.activeJob(kind) != nil }

func (a *app) downloading(key, target string) bool {
	return slices.ContainsFunc(a.jobs, func(task *job) bool {
		return task.kind == downloadJob && task.key == key && task.target == target
	})
}

func (a *app) gameInUse(i engine.Instance) bool {
	return i.ID != "" && (a.runningCount(i.ID) > 0 || a.launchTarget == i.ID)
}

func (a *app) inUse(i engine.Instance) bool {
	return a.gameInUse(i) || a.installInProgress(i)
}

func (a *app) installInProgress(i engine.Instance) bool {
	return i.ID != "" && slices.ContainsFunc(a.jobs, func(task *job) bool {
		return task.target == i.ID
	})
}

func (a *app) runningCount(id string) int {
	count := 0
	for _, instanceID := range a.running {
		if instanceID == id {
			count++
		}
	}
	return count
}

func (a *app) navigate(page string) {
	if a.page != page {
		if a.page == managePage {
			a.mods, a.modIcons = nil, nil
		}
		a.page = page
		a.mainScroll.Y = 0
	}
	switch page {
	case managePage:
		a.refreshInstances()
	case downloadsPage:
		a.downloadVersions(false)
	}
}

func (a *app) openVersions() {
	a.navigate(downloadsPage)
	a.resourceKind = "游戏版本"
	a.leaveProject()
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
		running: map[*engine.Game]string{}, resourceKind: "游戏版本",
		status:        "就绪",
		versionGroups: map[string]bool{},
		openLoaders:   map[string]bool{}, loaderLists: map[loaderKey][]engine.LoaderBuild{},
		buildStamps: map[loaderKey]time.Time{}, buildLoading: map[loaderKey]bool{}, buildErrors: map[loaderKey]string{},
		projectIcons: map[string]ui.ImageSource{}, openResourceGroups: map[loaderKey]bool{},
		resourceSearches: map[string]*resourceSearch{"mod": {}, "modpack": {}, "shader": {}, "datapack": {}, "resourcepack": {}},
	}
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
		a.selected = ""
		if len(a.instances) > 0 {
			a.selected = a.instances[0].ID
		}
	}
	a.renameValue = a.current().Name
	a.refreshMods()
	a.refreshSkinAvatar()
}

func (a *app) refreshMods() {
	a.mods, a.modIcons = nil, nil
	if a.page != managePage {
		return
	}
	if i := a.current(); i.ID != "" {
		var err error
		a.mods, err = a.engine.Mods(i)
		if err != nil {
			a.status = err.Error()
		}
		a.modIcons = make(map[string]ui.ImageSource)
		for index, mod := range a.mods {
			if len(mod.Icon) == 0 {
				continue
			}
			if icon, err := ui.DecodeBitmapScaled(mod.Icon, 88); err == nil {
				a.modIcons[mod.Filename] = icon
			}
			a.mods[index].Icon = nil
		}
		if len(a.mods) > 0 {
			a.preloadModNames()
		}
	}
}

func (a *app) selectInstance(i engine.Instance) {
	if a.selected == i.ID && a.settings.Instance == i.ID {
		return
	}
	a.selected = i.ID
	a.renameValue = i.Name
	a.settings.Instance = i.ID
	if err := a.engine.SaveSettings(a.settings); err != nil {
		a.status = err.Error()
	}
	a.modFilter = ""
	a.refreshMods()
	a.refreshSkinAvatar()
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
	if !a.busy(kind) {
		a.startJob(&job{kind: kind, title: string(kind), message: message}, work)
	}
}

func (a *app) startDownload(key, title, target, message string, work operation) {
	if key == "" || !a.downloading(key, target) {
		a.startJob(&job{kind: downloadJob, key: key, title: title, target: target, message: message}, work)
	}
}

func (a *app) startJob(task *job, work operation) {
	if a.closing || a.window == nil {
		return
	}
	kind := task.kind
	ctx, cancel := context.WithCancelCause(context.Background())
	task.cancel = func() { cancel(engine.ErrTaskCanceled) }
	a.jobs = append(a.jobs, task)
	go func() {
		defer cancel(nil)
		finish, err := work(ctx, func(s string) {
			a.window.Update(func() {
				if !task.cancelling && slices.Contains(a.jobs, task) {
					task.message = s
				}
			})
		})
		a.window.Update(func() {
			a.jobs = slices.DeleteFunc(a.jobs, func(active *job) bool { return active == task })
			switch kind {
			case catalogJob:
				if a.activeSearch != nil {
					if !task.superseded {
						a.activeSearch.failed = err != nil
					}
					a.activeSearch = nil
				}
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
			if task.superseded {
				return
			}
			if err != nil {
				if kind == downloadJob || kind == importJob {
					a.refreshInstances()
				}
				a.status = err.Error()
				if _, cleanupFailed := err.(interface{ Unwrap() []error }); task.cancelling && !cleanupFailed {
					a.status = string(kind) + "已取消。"
				}
			} else if finish != nil {
				finish()
			}
			if task.onDone != nil {
				task.onDone(err)
			}
		})
	}()
}

func (a *app) install() {
	i := a.installSelection()
	title := strings.TrimSpace(i.Name)
	if title == "" {
		title = i.DefaultName()
	}
	a.startDownload(versionDownloadKey(i), title, "", "正在准备安装 Minecraft…", func(ctx context.Context, status func(string)) (func(), error) {
		i, err := a.engine.Install(ctx, i, status)
		return func() { a.installed(i) }, err
	})
}

func versionDownloadKey(i engine.Instance) string {
	return strings.Join([]string{"game", i.Version, i.Loader, i.Build, strings.TrimSpace(i.Name)}, "\x00")
}

func (a *app) play(i engine.Instance) {
	if i.ID == "" || a.installInProgress(i) || a.busy(launchJob) || a.closing || a.window == nil {
		return
	}
	account, err := engine.LocalAccount(strings.TrimSpace(a.nickname))
	if err != nil {
		a.status = err.Error()
		return
	}
	reserved := 0
	for game := range a.running {
		reserved += game.Memory
	}
	options := engine.LaunchOptions{
		Additional: a.runningCount(i.ID) > 0,
		Skin:       a.settings.Skin, SkinPNG: a.settings.SkinPNG,
	}
	a.stopLaunch = false
	a.launchTarget = i.ID
	a.start(launchJob, "正在准备启动…", func(ctx context.Context, status func(string)) (func(), error) {
		memory, err := engine.AutomaticMemory(i.Loader != "", reserved)
		if err != nil {
			return nil, err
		}
		options.Memory = memory
		game, err := a.engine.Launch(ctx, i, account, options, status)
		return func() {
			if a.closing {
				_ = game.Stop()
				return
			}
			a.running[game] = i.ID
			a.status = fmt.Sprintf("Minecraft 已启动 · %s · 分配 %d MiB", i.Name, memory)
			if a.stopLaunch {
				a.stopGames()
			}
			go func() {
				err := <-game.Done
				a.window.Update(func() {
					delete(a.running, game)
					if !a.busy(launchJob) {
						a.status = "游戏已退出 · " + i.Name
						if game.Stopping {
							a.status = "已结束 Minecraft · " + i.Name
						} else if err != nil {
							a.status = "游戏异常退出，请在游戏目录查看 " + filepath.Base(game.LogPath) + "。"
						}
					}
				})
			}()
		}, err
	})
}

func (a *app) stopGames() {
	if task := a.activeJob(launchJob); task != nil {
		a.stopLaunch = true
		task.cancelling = true
		task.message = "正在取消启动…"
		task.cancel()
	}
	a.status = "正在结束 Minecraft 实例…"
	for game := range a.running {
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
	case "数据包":
		return "datapack"
	case "资源包":
		return "resourcepack"
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
	query, submitted, game, loader, source string
	results                                []engine.Project
	page, total, requested                 int
	loaded, failed, pending                bool
	cache                                  map[searchKey]searchResult
}

type searchKey struct {
	query, game, loader, source string
	page                        int
}

type searchResult struct {
	hits  []engine.Project
	total int
	at    time.Time
}

func (a *app) search(page int) {
	kind := a.kind()
	if kind == "" || a.busy(catalogJob) || a.window == nil || a.closing {
		return
	}
	state := a.resourceSearches[kind]
	state.requested, state.failed, state.pending = page, false, false
	key := searchKey{state.submitted, state.game, state.loader, state.source, page}
	show := func(result searchResult) {
		state.results, state.total, state.page, state.loaded = result.hits, result.total, page, true
		if a.page == downloadsPage && a.kind() == kind {
			a.mainScroll.Y = 0
		}
		a.fetchProjectIcons(result.hits)
		if kind == "mod" && len(result.hits) > 0 {
			a.preloadModNames()
		}
		a.status = "搜索完成。"
	}
	if result, ok := state.cache[key]; ok && time.Since(result.at) < 5*time.Minute {
		show(result)
		return
	}
	a.activeSearch = state
	lookup := a.loadModNames
	search, source := engine.Search, "Modrinth"
	if key.source == "CurseForge" {
		search, source = engine.SearchCurseForge, "CurseForge"
	}
	a.start(catalogJob, "正在搜索 "+source+"…", func(ctx context.Context, _ func(string)) (func(), error) {
		query := key.query
		var names *engine.ModNames
		translatedSearch := kind == "mod" && lookup != nil && strings.ContainsFunc(query, func(r rune) bool { return unicode.Is(unicode.Han, r) })
		if translatedSearch {
			names = lookup()
			if slug := names.SearchSlug(query); slug != "" {
				query = strings.ReplaceAll(slug, "-", " ")
			}
		}
		searchCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		hits, total, err := search(searchCtx, kind, query, engine.Instance{Version: key.game, Loader: key.loader}, page)
		return func() {
			if state.pending {
				return
			}
			if names != nil {
				a.modNames = names
			}
			result := searchResult{hits, total, time.Now()}
			if state.cache == nil || len(state.cache) >= 12 {
				state.cache = make(map[searchKey]searchResult)
			}
			if !translatedSearch || names != nil {
				state.cache[key] = result
			}
			show(result)
		}, err
	})
}

func (a *app) submitResourceSearch(state *resourceSearch) {
	query := strings.TrimSpace(state.query)
	if a.activeSearch == state && state.submitted == query {
		return
	}
	if task := a.activeJob(catalogJob); a.activeSearch != nil && task != nil && !task.cancelling {
		task.superseded = true
		task.cancel()
	}
	state.submitted = query
	state.results, state.page, state.total, state.loaded = nil, 0, 0, false
	state.pending = true
	a.search(0)
}

func (a *app) loadProject(p engine.Project) {
	kind := a.kind()
	state := a.resourceSearches[kind]
	if isCurseProject(p) {
		a.loadCursePage(p, state.game, state.loader, 0)
		return
	}
	cacheKey := kind + ":" + p.ID
	show := func(list []engine.ResourceVersion) {
		a.project, a.resourceGroups = p, groupResourceVersions(list, a.versionList, kind)
		a.fileGame, a.fileLoader = state.game, state.loader
		a.resourceTarget = a.selected
		a.datapackInstance = ""
		clear(a.openResourceGroups)
		a.mainScroll.Y = 0
		a.status = "项目版本已加载。"
	}
	if cached, ok := a.projectVersions[cacheKey]; ok && time.Since(cached.at) < 5*time.Minute {
		show(cached.versions)
		return
	}
	a.start(catalogJob, "正在读取项目版本…", func(ctx context.Context, _ func(string)) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		list, err := engine.ResourceVersions(ctx, p.ID, kind, engine.Instance{})
		return func() {
			if len(list) <= 256 {
				if a.projectVersions == nil || len(a.projectVersions) >= 8 {
					a.projectVersions = make(map[string]projectVersionResult)
				}
				a.projectVersions[cacheKey] = projectVersionResult{list, time.Now()}
			}
			if a.page != downloadsPage || a.kind() != kind {
				return
			}
			show(list)
		}, err
	})
}

func isCurseProject(p engine.Project) bool {
	return strings.HasPrefix(p.ID, "curseforge:")
}

func (a *app) loadCursePage(project engine.Project, game, loader string, page int) {
	kind := a.kind()
	current := a.project.ID
	a.start(catalogJob, "正在读取 CurseForge 项目版本…", func(ctx context.Context, _ func(string)) (func(), error) {
		ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
		defer cancel()
		list, more, err := engine.CurseForgeVersions(ctx, project.ID, kind, engine.Instance{Version: game, Loader: loader}, page)
		return func() {
			if a.page != downloadsPage || a.kind() != kind || a.project.ID != current {
				return
			}
			if current == "" {
				a.resourceTarget = a.selected
				a.datapackInstance = ""
			}
			a.project, a.resourceGroups = project, groupResourceVersions(list, a.versionList, kind)
			a.projectPage, a.projectMore = page, more
			a.fileGame, a.fileLoader = game, loader
			clear(a.openResourceGroups)
			a.mainScroll.Y = 0
			a.status = "项目版本已加载。"
		}, err
	})
}

type projectVersionResult struct {
	versions []engine.ResourceVersion
	at       time.Time
}

func (a *app) installResource(v engine.ResourceVersion, i engine.Instance) {
	kind := a.kind()
	world := ""
	if kind == "datapack" {
		world = a.datapackWorld
	}
	if kind != "modpack" && (a.gameInUse(i) || !resourceMatches(kind, i, v)) {
		return
	}
	if kind == "datapack" && (world == "" || i.ID != a.datapackInstance) {
		return
	}
	title := v.Name
	if i.ID != "" {
		title += " · " + i.Name
	}
	a.startDownload(resourceDownloadKey(kind, v.ID, world), title, i.ID, "正在准备下载…", func(ctx context.Context, status func(string)) (func(), error) {
		if kind == "modpack" {
			pack, err := a.engine.DownloadPack(ctx, v, status)
			return func() { a.installed(pack) }, err
		}
		var err error
		if kind == "datapack" {
			err = a.engine.InstallDatapack(ctx, i, world, v, status)
		} else {
			err = a.engine.InstallResource(ctx, kind, i, v, status)
		}
		return func() {
			if kind == "mod" {
				a.refreshMods()
			}
			a.status = "已安装 " + v.Name
		}, err
	})
}

func resourceDownloadKey(kind, id, world string) string {
	return strings.Join([]string{kind, id, world}, "\x00")
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
	case "neoforge":
		return "NeoForge"
	default:
		return "原版"
	}
}

func (a *app) hasVersion() bool {
	return slices.ContainsFunc(a.versionList, func(v engine.Version) bool { return v.ID == a.targetVersion })
}

func (a *app) preloadModNames() {
	if a.modNames != nil || a.modNamesLoading || a.loadModNames == nil || a.window == nil {
		return
	}
	a.modNamesLoading = true
	go func() {
		names := a.loadModNames()
		if a.catalogContext.Err() == nil {
			a.window.Update(func() {
				a.modNamesLoading = false
				a.modNames = names
			})
		}
	}()
}

func main() {
	// The launcher UI can render on the CPU without loading graphics drivers.
	if _, configured := os.LookupEnv("MYGO_GPU"); !configured {
		_ = os.Setenv("MYGO_GPU", "0")
	}
	data, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exeDir := filepath.Dir(exe)
	gameRoot := filepath.Join(exeDir, ".minecraft")
	a := newApp(engine.Engine{DataDir: filepath.Join(data, "HJML"), GameRoot: gameRoot, JavaRoot: filepath.Join(exeDir, "jdk")})
	catalogCtx, stopCatalog := context.WithCancel(context.Background())
	defer stopCatalog()
	a.catalogContext = catalogCtx
	var namesMu sync.Mutex
	var names *engine.ModNames
	var retryNamesAfter time.Time
	a.loadModNames = func() *engine.ModNames {
		namesMu.Lock()
		defer namesMu.Unlock()
		if names != nil || time.Now().Before(retryNamesAfter) {
			return names
		}
		ctx, cancel := context.WithTimeout(catalogCtx, 8*time.Second)
		defer cancel()
		var err error
		names, err = a.engine.LoadModNames(ctx)
		if err != nil {
			retryNamesAfter = time.Now().Add(10 * time.Second)
			log.Printf("读取模组中文索引：%v", err)
		}
		return names
	}
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
		a.refreshSkinAvatar()
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

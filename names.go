package main

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// Names use stable Modrinth slugs and Fabric mod IDs where available.
var chineseNames = map[string]string{
	"fabric-api": "Fabric 基础接口", "sodium": "钠", "sodium-extra": "钠扩展",
	"iris": "虹彩光影", "entityculling": "实体渲染剔除", "lithium": "锂",
	"ferrite-core": "铁氧体核心", "ferritecore": "铁氧体核心",
	"immediatelyfast": "立即优化", "modmenu": "模组菜单", "mod-menu": "模组菜单",
	"cloth-config": "布料配置接口", "cloth-config-api": "布料配置接口",
	"architectury-api": "建筑师接口", "architectury": "建筑师接口",
	"modernfix": "现代化修复", "indium": "铟", "phosphor": "磷", "starlight": "星光",
	"lazydfu": "惰性数据修复", "dynamic-fps": "动态帧率", "no-chat-reports": "禁用聊天举报",
	"appleskin": "苹果皮", "jei": "物品管理器", "rei": "粗略物品管理器",
	"emi": "优雅模组物品栏", "create": "机械动力", "create-fabric": "机械动力",
	"waystones": "传送石碑", "jade": "玉", "wthit": "这是什么",
	"journeymap": "旅行地图", "xaeros-minimap": "Xaero 的小地图",
	"xaeros-world-map": "Xaero 的世界地图", "terralith": "地形大修",
	"biomes-o-plenty": "超多生物群系", "yungs-better-dungeons": "YUNG 的更好的地牢",
	"betterf3": "更好的调试界面", "mouse-tweaks": "鼠标手势",
	"craftable-nametag": "可合成的命名牌", "natures-melody": "自然旋律", "pet-names": "宠物名字",
	"continuity": "连续纹理", "lambdynamiclights": "动态光源",
	"c2me-fabric": "并发区块管理引擎", "krypton": "氪", "noisium": "噪声生成优化",
	"simple-voice-chat": "简易语音聊天", "zoomify": "缩放望远",
	"fabulously-optimized": "极致优化", "simply-optimized": "简约优化",
	"additive": "增强型优化包", "adrenaline": "肾上腺素", "fabulously-modded": "极致模组化",
	"better-mc-fabric-bmc2": "更好的 Minecraft（Fabric）", "better-mc-forge-bmc4": "更好的 Minecraft（Forge）",
	"cobblemon": "方块宝可梦", "cobblemon-fabric": "方块宝可梦",
	"cobblemon-official-modpack": "方块宝可梦官方整合包", "all-the-mods-10": "全模组 10",
	"complementary-reimagined": "互补光影·重构", "complementary-unbound": "互补光影·无界",
	"bsl-shaders": "BSL 光影", "makeup-ultra-fast-shaders": "MakeUp 极速光影",
	"solas-shader": "Solas 光影", "rethinking-voxels": "体素重构光影",
	"vanilla-plus-shader": "原版增强光影", "photon-shader": "光子光影",
	"faithful-32x": "忠实原版 32×", "bare-bones": "极简原版材质",
	"fresh-animations": "鲜活动画", "stay-true": "保留原味", "default-dark-mode": "默认深色界面",
}

func bilingualName(title, slug string) string {
	if chinese := projectChineseName(slug); chinese != "" && chinese != title {
		return chinese + " | " + title
	}
	return title
}

func projectChineseName(slug string) string {
	return chineseNames[strings.ReplaceAll(strings.ToLower(slug), "_", "-")]
}

func compactSearch(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

func pinyinKeys(text string) (string, string) {
	args := pinyin.NewArgs()
	args.Fallback = func(r rune, _ pinyin.Args) []string { return []string{string(r)} }
	var full, initials strings.Builder
	for _, syllable := range pinyin.LazyPinyin(text, args) {
		full.WriteString(syllable)
		for _, r := range syllable {
			initials.WriteRune(r)
			break
		}
	}
	return compactSearch(full.String()), compactSearch(initials.String())
}

func matchesSearch(text, query string) bool {
	if strings.Contains(strings.ToLower(text), strings.ToLower(strings.TrimSpace(query))) {
		return true
	}
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		return false
	}
	key := compactSearch(query)
	full, initials := pinyinKeys(text)
	return key != "" && (strings.Contains(full, key) || strings.Contains(initials, key))
}

func searchName(query string) string {
	if chinese, english, ok := strings.Cut(query, " | "); ok &&
		slices.Contains(slices.Collect(maps.Values(chineseNames)), chinese) {
		return strings.TrimSpace(english)
	}
	key := compactSearch(query)
	if key == "" {
		return query
	}
	var partial string
	ambiguous := false
	for _, slug := range slices.Sorted(maps.Keys(chineseNames)) {
		name := chineseNames[slug]
		full, initials := pinyinKeys(name)
		if key == compactSearch(name) || key == full || key == initials {
			return strings.ReplaceAll(slug, "-", " ")
		}
		if len([]rune(key)) >= 3 && (strings.HasPrefix(full, key) || strings.HasPrefix(initials, key) || strings.HasPrefix(name, query)) {
			if partial != "" && chineseNames[partial] != name {
				ambiguous = true
			}
			if partial == "" {
				partial = slug
			}
		}
	}
	if partial != "" && !ambiguous {
		return strings.ReplaceAll(partial, "-", " ")
	}
	return query
}

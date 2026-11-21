# HaoJieMinecraftLauncher

HJML 是用 Go 和 MyGo 开发的 Minecraft Java 版启动器，支持 Windows x64、离线玩家、独立游戏实例及 Modrinth、CurseForge 资源下载。

## 快速开始

1. 从 [GitHub Releases](https://github.com/AAAjct/HaoJieMinecraftLauncher/releases/latest) 下载 `HJML.exe`，放在有写入权限的文件夹后运行。Windows 文件属性中的版权为 Jct，语言为简体中文。
2. 在 **下载 → 游戏版本** 中创建实例。支持原版，以及 Fabric、采用现代安装格式的 Forge 和 NeoForge。启动器在需要时优先从国内镜像下载 Temurin JDK 8、17、21 或 25，也可复用已安装的 Zulu JDK。要求 Java 16 的 Minecraft 版本使用 Java 17。
3. 在 **启动** 页填写离线玩家名，选择实例并启动。所需文件下载齐全后可以离线运行。游戏运行时，右下角的 **×** 可结束由启动器启动的所有 Minecraft 实例。

- 点击 **再次启动** 可多开同一实例。各进程单独计算可分配内存。追加启动使用独立的游戏选项副本，共用实例的存档、模组等内容，日志保存在实例目录下的 `hjml-launch-*.log`。
- 右键头像可切换史蒂夫、艾利克斯或导入本地 Minecraft PNG 皮肤（64×64 或 64×32）。头像加载期间保持空白，已导入皮肤在菜单中显示去掉扩展名后的文件名。皮肤会在下次启动时通过本地资源包应用到离线玩家。

## 下载与实例管理

| 下载分类 | 安装位置或方式 |
| --- | --- |
| 游戏版本、Modrinth 和 CurseForge 整合包 | 创建独立实例 |
| 模组、光影、资源包 | 安装到兼容实例的 `mods/`、`shaderpacks/`、`resourcepacks/` |
| 数据包 | 选择实例和现有世界，安装到该存档的 `datapacks/` |

- Minecraft 客户端、加载器下载和 Zulu JDK 会并行准备。游戏版本和资源也可以同时下载安装。打开 **任务** 查看总进度、下载速度、剩余文件和各阶段进度，也可单独取消任务。支持校验和续传的下载在任务内重试时可接着下载。任务结束时会清理对应的临时下载和续传文件。
- Minecraft 和 Modrinth 优先使用 BMCLAPI 和 MCIM 镜像。CurseForge 公共文件优先使用 mediafilez CDN，失败时尝试 MCIM 和 edge CDN。安装整合包时，如果 MCIM 超时且无法获得官方文件信息，启动器可通过 CFWidget 查找公开文件，并从 CurseForge CDN 下载，核对文件大小和压缩包结构。可在启动器进程的环境变量中设置自己的 `CURSEFORGE_API_KEY`，启用官方 API 回退。没有配置代理环境变量时，启动器使用 Windows 中已启用的手动代理。
- 在 **下载** 页可切换 Modrinth 或 CurseForge。模组和整合包的加载器筛选提供 Fabric、Forge 和 NeoForge。
- 在 **实例管理 → 我的实例 → 导入** 或 **下载 → 整合包 → 从文件安装** 选择整合包，也可直接拖入。支持含 Modrinth 或 CurseForge 清单的 ZIP、RAR，以及 Modrinth 的 MRPACK。导入会直接创建新实例。CurseForge 未提供下载地址时，启动器会根据文件 ID 和文件名尝试 CDN，并校验文件。
- 在 **实例管理 → 从文件安装** 可多选 JAR 模组。系统文件对话框确认后，模组会直接安装到打开对话框时选中的实例。直接拖入 JAR 时会显示目标实例确认。
- **实例管理** 支持改名、打开目录、刷新模组、启用、禁用及删除。
- 原版实例默认用 Minecraft 版本命名。手动安装带加载器的实例默认包含构建号，例如 `26.3-fabric_0.20.1`。整合包使用去掉扩展名的压缩包文件名作为版本目录及启动文件的名称。同名时会自动追加首个可用的 `(1)`、`(2)` 等编号。
- 模组列表优先显示中文名，其次使用 JAR 内声明的名称。图标、版本、标签和描述取自本地模组元数据。中文名称与检索使用 [PCL Community 提供的 MCMOD 索引](https://github.com/PCL-Community/PCL-CE/blob/676a752d304165e8c5fd3729841f1582bbf4b795/Plain%20Craft%20Launcher%202/Resources/mcmod.buf)。

## 数据位置

| 位置 | 内容 |
| --- | --- |
| EXE 同级的 `.minecraft/` | 共享运行库、资源、`.hjml-cache/` 中的共享客户端，以及 `versions/<实例目录>/` 下的实例数据 |
| `.minecraft/.hjml-cache/temp/` | 临时下载、续传分片及安装器工作文件，使用结束后清理 |
| EXE 同级的 `jdk/temurin <主版本>/`、`jdk/zulu <主版本>/` | 已安装的 JDK |
| `%LOCALAPPDATA%\HJML` | 启动器设置、版本目录缓存以及旧版实例 |
| 实例目录的 `hjml-latest.log` | 最近一次游戏启动日志 |

## 源码与构建

根目录的 Go 文件负责 MyGo 界面与交互，`internal/engine/` 负责 Minecraft、Java 和资源安装，`resources/` 存放内嵌图片。

在 Windows x64 上使用 Go 1.27.4 构建。

```powershell
go test ./...
.\build.ps1
```

输出文件为 `dist/windows-amd64/HJML.exe`。安装 UPX 后可运行 `.\build.ps1 -Compress -DistributionDirectory dist/compact`，生成更小的单 EXE。GitHub Releases 提供压缩后的 Windows x64 单文件。要运行涉及真实下载及游戏启动的联网集成测试，可设置 `HJML_INTEGRATION=1` 后运行 `go test -v -timeout 15m ./...`。

需要内置 CurseForge API Key 时，调用 `build.ps1 -CurseForgeKeyFromStdin`，由调用进程通过标准输入传递密钥。密钥会编入 EXE，随该构建分发。普通构建不携带密钥。

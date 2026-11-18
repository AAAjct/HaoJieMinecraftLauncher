# HaoJieMinecraftLauncher

HJML 是用 Go 和 MyGo 开发的 Minecraft Java 版启动器，支持 Windows x64、离线玩家、独立游戏实例和 Modrinth 资源下载。

## 快速开始

1. 从 [GitHub Releases](https://github.com/AAAjct/HaoJieMinecraftLauncher/releases/latest) 下载 `HJML.exe`，放在有写入权限的文件夹后运行。
2. 在 **下载 → 游戏版本** 中创建实例。支持原版、Fabric、Quilt 和采用现代安装格式的 Forge。启动器自动下载游戏文件和对应版本的 Azul Zulu JDK。
3. 在 **启动** 页填写离线玩家名，选择实例并启动。所需文件下载齐全后可以离线运行。游戏运行时，右下角的 **×** 可结束由启动器启动的所有 Minecraft 实例。

## 下载与实例管理

| 下载分类 | 安装位置或方式 |
| --- | --- |
| 游戏版本、Modrinth 整合包 | 创建独立实例 |
| 模组、光影、资源包 | 安装到兼容实例的 `mods/`、`shaderpacks/`、`resourcepacks/` |
| 数据包 | 选择实例和现有世界，安装到该存档的 `datapacks/` |
| 世界 | 粘贴 Java 版 ZIP 的 HTTPS 下载直链，解压到目标实例的 `saves/` |

- 游戏版本和资源可以同时下载安装。打开 **任务** 查看各项进度或单独取消。支持校验且下载源允许续传时，重新开始同一安装任务可使用已下载的部分。
- **实例管理** 支持改名、打开目录、刷新模组、启用、禁用及删除。可拖入 JAR 或 Modrinth 整合包，也可通过 **从文件安装** 选择文件。
- 模组列表优先显示中文名，其次使用 JAR 内声明的名称。图标、版本、标签和描述取自本地模组元数据。中文名称与检索使用 [PCL Community 提供的 MCMOD 索引](https://github.com/PCL-Community/PCL-CE/blob/676a752d304165e8c5fd3729841f1582bbf4b795/Plain%20Craft%20Launcher%202/Resources/mcmod.buf)。
- **下载 → 世界** 提供 [Minecraft Maps](https://www.minecraftmaps.com/versions) 浏览入口。请按实例的 Minecraft 版本选择地图。安装时保留已有同名存档。

## 数据位置

| 位置 | 内容 |
| --- | --- |
| EXE 同级的 `.minecraft/` | 共享运行库、资源缓存、续传文件和 `versions/<实例目录>/` 下的实例数据 |
| EXE 同级的 `Jdk/Zulu <主版本>/` | 自动安装的 Zulu JDK，例如 `Jdk/Zulu 17/` 和 `Jdk/Zulu 21/` |
| `%LOCALAPPDATA%\HJML` | 启动器设置、版本目录缓存以及旧版实例 |
| 实例目录的 `hjml-latest.log` | 最近一次游戏启动日志 |

## 源码与构建

根目录的 Go 文件负责 MyGo 界面与交互，`internal/engine/` 负责 Minecraft、Java 和资源安装，`resources/` 存放内嵌图片。

在 Windows x64 上使用 Go 1.27.4 构建。

```powershell
go test ./...
.\build.ps1
```

输出文件为 `dist/windows-amd64/HJML.exe`。要运行涉及真实下载及游戏启动的联网集成测试，可设置 `HJML_INTEGRATION=1` 后运行 `go test -v -timeout 15m ./...`。

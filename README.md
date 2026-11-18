# HaoJieMinecraftLauncher

HJML 是使用 Go 和 MyGo 开发的 Windows x64 Minecraft Java 版启动器。它提供离线玩家启动、独立实例、游戏与加载器安装，以及 Modrinth 模组、整合包和光影下载。

## 下载和使用

1. 从 [GitHub Releases](https://github.com/AAAjct/HaoJieMinecraftLauncher/releases/tag/1.0.0) 下载 `HaoJieMinecraftLauncher.exe`，放到有写入权限的目录后双击运行。
2. 在 **下载 → 游戏版本** 中安装原版，或选择 Fabric、Quilt、Forge。Forge 支持采用现代安装格式的版本。启动器会自动安装所需的 Azul Zulu JDK。
3. 返回 **启动**，填写离线玩家名并选择实例。首次安装需要联网，文件下载齐全后可离线启动。

## 实例和资源

- 点击 **实例管理** 刷新实例和模组列表。选中实例后，可直接编辑并保存名称、打开目录、安装本地文件，或删除实例。删除实例需要确认，单个模组点击 **删除** 后立即移除。
- 支持拖入 JAR 模组和 Modrinth 整合包，也可通过 **从文件安装** 选择文件。禁用的模组显示为灰色删除线，仍可随时启用。
- 模组支持按游戏版本和加载器筛选。选择文件后，只有游戏版本与加载器兼容且空闲的实例才能接收安装。若文件对应另一个可用实例，文件行会标明安装目标。
- 中文模组检索参考 [PCL Community 的中文搜索逻辑](https://github.com/PCL-Community/PCL-CE/pull/3278)，使用其 [MCMOD 名称索引](https://github.com/PCL-Community/PCL-CE/blob/676a752d304165e8c5fd3729841f1582bbf4b795/Plain%20Craft%20Launcher%202/Resources/mcmod.buf) 匹配中文名，再用最佳匹配的项目关键词查询 Modrinth。首次联网获取索引后会在本地缓存。
- 在 **任务** 查看下载进度、速度和待处理文件。客户端、运行库和游戏资源共用并行队列，支持分段的较大文件会并行下载。

## 文件位置

| 路径 | 内容 |
| --- | --- |
| EXE 同级的 `.minecraft/` | 共享游戏资源、中文模组索引和 `versions/<实例目录>/` 中的独立游戏数据 |
| `%LOCALAPPDATA%\HJML` | 启动器设置、Java、版本目录缓存，以及旧版实例 |
| 实例目录的 `hjml-latest.log` | 最近一次游戏启动的日志 |

## 构建和测试

在 Windows x64 上安装 Go 1.27.4，然后从仓库根目录运行：

```powershell
go test ./...
.\build.ps1
```

MyGo 构建产物为 `dist/windows-amd64/HaoJieMinecraftLauncher.exe`，Windows 图标嵌入 EXE。根目录的 Go 文件负责界面与交互，`internal/engine/` 负责游戏、Java 和资源管理，`resources/` 保存头像和 HJ 图标。

联网集成测试可以验证真实的游戏目录、模组下载和 Minecraft 启动。默认使用 Fabric，设定 `HJML_TEST_LOADER=forge` 可测试 Forge。

```powershell
$env:HJML_INTEGRATION = '1'
go test -v -timeout 15m ./...
Remove-Item Env:HJML_INTEGRATION
```

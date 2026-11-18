# HaoJieMinecraftLauncher（HJML）

面向 Windows x64 的 Minecraft Java 版启动器。可以安装原版游戏和模组加载器、浏览 Modrinth 资源，并为不同的玩法管理独立实例。界面使用 Go 和 MyGo，Rust 辅助工具随启动器一起构建并嵌入 EXE。

## 快速开始

1. 按下方步骤构建，运行 `dist/windows-amd64/HaoJieMinecraftLauncher.exe`。建议把 EXE 放在有写入权限的目录中，启动器会在旁边创建 `.minecraft`。
2. 在 **下载 → 游戏版本** 中选择 Minecraft 版本。可以直接安装原版，或展开 Forge、Fabric、Quilt 并选择加载器版本。
3. 安装完成后返回 **启动**，选择实例并启动游戏。在头像下方可以编辑离线玩家名。

首次安装需要联网下载游戏文件和 Java。所需文件齐全后，可以离线启动。当前仅支持离线玩家。

## 游戏与资源管理

| 功能 | 用法 |
| --- | --- |
| 游戏版本 | 分别浏览正式版、预览版和远古版。新建 Fabric 实例会默认安装 Fabric API。Forge 支持采用现代安装格式的版本。 |
| Modrinth | 在 **下载** 页浏览模组、整合包和光影，按游戏版本和适用的加载器筛选文件。安装模组时会检查实例兼容性并解析必需依赖。 |
| 本地导入 | 把模组 `.jar` 拖入窗口，再选择目标实例。拖入 `.mrpack` 或含 Modrinth 格式清单的 ZIP，可以创建新实例。也可以使用 **从文件安装**。 |
| 下载任务 | 在 **任务** 页查看阶段、速率和剩余文件。最多并行下载 32 个文件，支持 Range 的大文件可分四段下载。 |
| Java 与启动 | 按游戏版本自动下载对应的 Azul Zulu Java，并自动分配内存。可同时运行多个实例。用窗口右上角的关闭按钮退出时，会结束本次启动器会话启动的游戏。 |

## 数据位置

- **EXE 同级的 `.minecraft/`** 保存共享的 `assets/` 和 `libraries/`。各实例位于 `versions/<实例目录>/`，包含各自的模组、存档、资源包和光影。排查启动问题时，可查看实例目录中的 `hjml-latest.log`。
- **`%LOCALAPPDATA%\HJML`** 保存启动器设置、Java 和目录缓存。旧版 HJML 实例仍可从原位置使用。

## 从源码构建

在 Windows x64 上准备 PowerShell、Go 1.27.4、Rust MSVC 工具链和 Visual Studio C++ Build Tools。然后在项目根目录运行：

```powershell
rustup target add x86_64-pc-windows-msvc
.\build.ps1
```

构建脚本先生成并嵌入 Rust 辅助工具，再通过 MyGo 打包启动器。默认输出为 `dist/windows-amd64/HaoJieMinecraftLauncher.exe`。

## 运行测试

先完成构建，再运行默认测试：

```powershell
go test ./...
```

联网集成测试会下载并启动 Minecraft 1.21.1，也会验证版本目录、Fabric API 和 Modrinth 下载。游戏启动测试默认使用 Fabric。如需验证 Forge，在运行测试前设置 `$env:HJML_TEST_LOADER = 'forge'`。

```powershell
$env:HJML_INTEGRATION = '1'
go test -v -timeout 15m ./...
Remove-Item Env:HJML_INTEGRATION
```

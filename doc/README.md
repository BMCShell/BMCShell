# BMCShell 3.0 框架介绍

扩展包命名和新版创建参数见 [扩展命名与脚手架参考](SCAFFOLD-CLI.md)。

Library 发布包、数据同步及结构变更见 [Library 发布与同步参考](LIBRARY-PACKAGE.md)。

软件源配置和发版流程见 [软件源管理](SOFTWARE-SOURCES.md)。

环境变量命名、加载和插件读写规则见 [环境变量管理规范](ENVIRONMENT-REGISTRY.md)，字段定义见 [环境变量注册表 CSV](ENVIRONMENT-REGISTRY-v3.csv)。

## 主程序与终端适配

Go 主程序 `BMCShell` 负责命令解析、扩展清单、设置、依赖诊断和帮助渲染。Bash 适配层定义 `bmc` 命令，在当前终端应用 `bmc use` 的候选会话，管理提示符并在退出时恢复会话命令。主程序与适配层交换经校验的结构化数据。

ToolPlugin 的命令和解析器通过独立进程接入。Book、Library、Theme 和 Bundle 通过各自的清单与数据接口接入。扩展保持自己的 UUID 和版本，可以独立发布。外部工具继续由操作系统或终端的包管理器管理。

## 主仓结构

```text
BMCShell/
├── cmd/bmcshell/    Go 程序入口
├── internal/       cli、session、packages、settings、render、bookdb
├── shell/          Bash 适配层
├── doc/            框架介绍和开发说明
├── scripts/        发布打包工具
└── tests/          本地端到端验证
```

## 编译与打包

首版在 Windows x64 上原生构建 `BMCShell.exe`。主仓需要 Go 1.26 或更高版本。开发环境使用 MSYS2 UCRT64。安装 MSYS2 后，在 UCRT64 终端中安装工具链。[MSYS2 官方安装页](https://www.msys2.org/) · [MSYS2 Go 软件包](https://packages.msys2.org/packages/mingw-w64-ucrt-x86_64-go)

```bash
pacman -Syu
pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-go
source /ucrt64/etc/profile.d/go.sh
go version
gcc --version
go env GOROOT GOOS GOARCH
```

安装 Go 后，当前窗口可用 `source` 加载包提供的 `GOROOT` 设置，也可以重新打开 UCRT64 终端。目标应为 `windows` 和 `amd64`。[MSYS2 Go 打包脚本](https://github.com/msys2/MINGW-packages/blob/master/mingw-w64-go/PKGBUILD#L102-L104)

从主仓库运行 `go test ./...` 和 `go build -trimpath -o BMCShell.exe ./cmd/bmcshell`，然后在 Bash 里 `source ./shell/bmcshell.sh`。

打包脚本接收已经编译的目标二进制和 Bash 适配脚本。运行环境为 PowerShell 7.4 或更高版本。

```powershell
./scripts/pack-release.ps1 -Version 3.0.0-dev.3 `
  -Binary ./BMCShell.exe -ShellBridge ./shell/bmcshell.sh
```

脚本验证输入为 Windows x64 可执行文件，输出 Windows ZIP 和含 SHA256 的发布清单。包内收录主程序、Bash 适配层、文档、第三方声明和已选定的 `LICENSE` 文件。需要组合分发时，可用 `-Scaffold` 指定独立仓库发布的脚手架可执行文件。

## 创建与打包插件

在独立的 `BMCShell-Scaffold` 仓库中构建脚手架，再指定插件类型和一个新目录。

```powershell
go build -trimpath -o BMCShell-Scaffold.exe ./cmd/bmc-scaffold
./BMCShell-Scaffold.exe create --name bmcshell-book-my-book --display-name '我的工具书' -o ..
```

脚手架支持 `toolplugin`、`book`、`library`、`theme` 和 `bundle`。每个新项目获得独立 Git 仓库、UUID、包清单、帮助、变更记录、README 和打包脚本。生成 `bundle` 时用 `--member <UUID>` 逐项指定成员。生成 `toolplugin` 后按该项目的 README 构建示例 EXE。

在任一插件项目内运行 `pwsh -File ./scripts/pack-release.ps1` 即可输出独立 ZIP 和 SHA256 清单。Book 项目的数据为 `*.book.db`，使用 2.x 的 SQLite 工具书格式，包括 `book_meta` 及其声明的数据表。每份数据库仍对应一个独立 Book 包。

## 开发版的验证范围

`bmc plugin install` 支持通过包名或 UUID 从软件源安装，也支持用 `--file` 安装本地目录和 ZIP。工具包装在加载适配层或建立会话时注册。安装新工具后，可以重新加载适配层以注册全局命令，新会话会重新注册会话命令。

当前数据接口支持本地 Book 检索、Library 按键查询及通过共享 URL 同步 SQLite 快照。遇到结构变化时，可从软件源选择新版 Library 和工具联合升级。完整设置菜单和系统包管理器接入列入后续工作。

## V2 工具和代理

通用工具仓库为 `bmcshell-tool-ipmi`、`bmcshell-tool-redfish` 和 `bmcshell-tool-snmp`。SBMC 工具仓库为 `sbmc-tool-cidtool`、`sbmc-tool-smash` 和 `sbmc-tool-bssh`，CID 关联信息库为 `sbmc-library-cidmap`。CIDTool、SMASH 的清单依赖 IPMI。工具包的 Bash 入口运行在当前终端，适用于受信任的工具。包内帮助支持 `bmc tool <工具名>` 和 `bmc help tool <工具名> --usage`，包含参数、示例和使用说明。

`bmc use IP` 建立目标会话。需要用户名和密码的工具可通过 `bmc_v2_auth` 或对应插件的 `--auth` 交互输入。凭据随目标切换和退出清除。2.x 自动 CID 探测与密码库匹配属于设备解析流程。

`bmc proxy` 查看全局 SOCKS5 设置。`bmc proxy on` 使用本机 1080 端口，也可通过 `bmc proxy 127.0.0.1:1081` 选择端口。设置保存在用户配置中。`bmc ipmi-proxy` 管理当前 Bash 的 IPMI 专用地址，全局设置优先，`bmc proxy off` 同时清除本窗口的专用设置。

Redfish 通过 curl 的 SOCKS5 选项连接，SSH 使用框架的 TCP CONNECT 转发，IPMI、CID、SMASH 和 SNMP 使用框架的 UDP ASSOCIATE 转发。代理需支持免认证，UDP 工具还需要代理节点可访问设备 UDP 端口。转发出错时返回错误。通用解析器只选目标 IP，使用代理的网络操作由各工具发起。

## 运行端到端验证

在 UCRT64 中分别构建主程序和脚手架后，从主仓库执行以下命令。默认使用同级 `BMCShell-Scaffold` 仓库的 EXE，也可以用 `BMCSHELL_SCAFFOLD` 指定路径。测试通过脚手架生成五种临时扩展包，使用临时配置目录和示例 IP。

```bash
BMCSHELL_HOME="$(cygpath -w "$(mktemp -d)")" bash tests/smoke.sh
```

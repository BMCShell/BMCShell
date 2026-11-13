# BMCShell 3.0 框架介绍

扩展包命名和创建方法见 [扩展命名与脚手架参考](SCAFFOLD-CLI.md)。

业务库发布和升级见 [Library 发布包与升级](LIBRARY-PACKAGE.md)。扩展索引配置和发版见 [软件源管理](SOFTWARE-SOURCES.md)。当前命令与参数可用 `bmc help <子命令>` 查看。

环境变量命名、加载和插件读写规则见 [环境变量管理规范](ENVIRONMENT-REGISTRY.md)，字段定义见 [环境变量注册表 CSV](ENVIRONMENT-REGISTRY-v3.csv)。

## 主程序与终端适配

Go 主程序 `BMCShell` 负责命令解析、扩展清单、设置、依赖诊断和帮助渲染。Bash 适配层定义 `bmc` 命令，在当前终端应用 `bmc use` 的候选会话，管理提示符并在退出时恢复会话命令。主程序与适配层交换经校验的结构化数据。

Tool 包可提供终端命令和解析器。Book、Library、Theme 和 Bundle 通过各自的清单与数据接口接入。扩展保持自己的内部 UUID 和版本，可以独立发布。外部工具继续由操作系统或终端的包管理器管理。

## 主仓结构

```text
BMCShell/
├── cmd/bmcshell/    Go 程序入口
├── internal/       cli、session、packages、settings、render、bookdb
├── shell/          Bash 适配层
├── doc/            框架介绍和开发说明
├── scripts/        发布打包工具
├── tests/          本地端到端验证
├── build/          本地编译产物
└── dist/           发布 ZIP 和校验清单
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

在主仓库的 Bash 终端编译并加载。

```bash
go test ./...
mkdir -p build
go build -trimpath -o build/BMCShell.exe ./cmd/bmcshell
source ./shell/bmcshell.sh
```

打包脚本默认从仓库读取 `build/BMCShell.exe` 和 `shell/bmcshell.sh`。可用 `-Binary` 和 `-ShellBridge` 指定其他文件。运行环境为 PowerShell 7.4 或更高版本。

```powershell
./scripts/pack-release.ps1 -Version 3.0.0-dev.9
```

脚本验证输入为 Windows x64 可执行文件，核对程序自报版本和 `-Version` 一致，再将 Windows ZIP 和含 SHA256 的发布清单写入 `dist/`。解压后的 `BMCShell/` 使用 `bin/bmcshell.sh` 作为 Bash 启动入口，核心程序位于 `bin/releases/<版本>/`。包内还收录文档、第三方声明和已选定的 `LICENSE` 文件。需要组合分发时，可用 `-Scaffold` 指定独立仓库发布的脚手架可执行文件。

## 安装后数据位置

使用发布包时，`bin/active` 选择 `bin/releases/<框架版本>/` 中的程序。数据根目录默认与解压根目录相同，可由 `BMCSHELL_HOME` 指定。

- `framework.db` 登记活动包版本和相应的 Library 工作库。Book 的查询规则继续保存在该书的 `book_meta`。
- `books/<UUID>/<包版本>/` 保存完整 Book 包。Tool、Theme 和 Bundle 位于 `plugins/<类型>/<UUID>/<包版本>/`。
- `libraries/<UUID>/releases/<包版本>/` 保存 Library 发布模板。`libraries/<UUID>/gen-…/` 保存工作库、发布基准和审计记录。
- 用户设置与软件源配置位于 `settings.json`，软件源索引缓存位于 `catalog/`。首次保存软件源前仍可读取旧的 `catalog/sources.json`。

## 创建与打包插件

在独立的 `BMCShell-Scaffold` 仓库中构建脚手架，再指定插件类型和一个新目录。

```powershell
go build -trimpath -o BMCShell-Scaffold.exe ./cmd/bmc-scaffold
./BMCShell-Scaffold.exe create --name bmcshell-book-my-book --display-name '我的工具书' -o ..
```

脚手架支持 `toolplugin`、`book`、`library`、`theme` 和 `bundle`。每个新项目获得独立 Git 仓库、UUID、包清单、帮助、变更记录、README 和打包脚本。生成 `bundle` 时用 `--member <UUID>` 逐项指定成员。生成 `toolplugin` 后按该项目的 README 构建示例 EXE。

在任一插件项目内运行 `pwsh -File ./scripts/pack-release.ps1` 即可输出独立 ZIP 和 SHA256 清单。Book 项目的数据命名为 `book/<shortName>.book.db`，使用 2.x 的 SQLite 工具书格式，包括 `book_meta` 及其声明的数据表。每份数据库仍对应一个独立 Book 包。

### 主题参数

主题可在 `bmc-package.json` 的 `settings` 数组中声明参数。启用主题后，`bmc setting` 的「外观」会加载当前主题的参数。枚举和布尔值使用编号选择，整数和文本在保存时校验。

```json
{
  "settings": [
    {
      "key": "glyphs",
      "label": "符号样式",
      "type": "enum",
      "default": "auto",
      "options": [
        { "value": "auto", "label": "自动" },
        { "value": "ascii", "label": "ASCII" }
      ]
    },
    { "key": "blank_line", "label": "命令间留空行", "type": "boolean", "default": true }
  ]
}
```

每项需要唯一的 `key`、显示名 `label`、`type` 和 `default`。键以小写字母开头，可含小写字母、数字及下划线，`active` 是框架保留键。支持 `enum`、`boolean`、`integer` 和 `string`。枚举默认值须匹配某个选项。布尔和整数默认值使用 JSON 布尔值和整数，整数可通过 `min`、`max` 限制范围。字符串使用可打印的单行文本。

`settings` 也适用于解析器及其他插件。可用 `path` 指定「插件设置」中的点分分组，例如 `connection.resolvers`。设置及枚举选项可用 `hint` 声明选中时显示的单行说明。可选的 `env` 声明重载后导出的环境变量，名称使用发布方命名空间，并且在同一安装中唯一。已安装主题和解析器导出分别进入 `theme.active` 与 `use.default_resolver` 的选择列表。

`bmc setting --get theme.glyphs` 读取当前主题参数，`bmc setting --set theme.glyphs ascii` 保存，`bmc setting --reset theme.glyphs` 恢复清单默认值。也可用 `plugin.<包名或 UUID>.<参数键>` 指定插件。框架按包 UUID 保存修改。插件升级后，失效的值回退到新版本的默认值，`bmc setting --check` 会提示已安装插件的失效值。

在设置中心用方向键移动，Enter 确认，Esc 返回。确认的修改进入草稿，可在意外退出后恢复。选择「保存并退出」提交所有改动并重载当前 Bash 的设置环境。命令行修改直接写入，末尾附加 `--reload` 可在成功后重载，也可单独运行 `bmc setting --reload`。

声明参数的主题使用 `.bashrc.perso` 作为 `data` 入口。Bash 在调用 `_bmcshell_theme_render` 时保留现有五个位置参数，从第六个参数开始按清单顺序逐项传入 `key=value`。主题通过 `shift 5` 和 `for option in "$@"` 读取参数，按第一个等号拆分键和值。

## 扩展与业务库

在 `bmc setting` 中配置扩展的软件源。用 `bmc plugin update` 刷新源索引。`bmc plugin list` 查看各源缓存的全部包版本并标记已安装的版本，加 `--installed` 只查看已安装的包，加 `-d` 显示包描述。用 `bmc plugin install` 按包名和版本安装，或加 `--file` 从本地路径安装。两种方式都可一次安装多个包。工具命令在加载适配层或建立会话时注册。

`bmc book <短名称>` 查看工具书目录，支持只读检索。用 `bmc library <短名称>` 查看业务库并进入交互管理。Library 升级由 `bmc plugin upgrade` 发起。框架根据本地工作库和两版发布数据计算变更，覆盖更新前备份并确认。

### Book 的共享环境字段替换

SNMP 分组登记 `snmp.enterprises` / `BMCSHELL_SNMP_ENTERPRISES`。完整 OID 模板使用 `1.3.6.1.4.1.enterprises`，将企业编号 `54357`、`37945` 或 `0` 替换到 `enterprises`，保留后缀。点分值替换完整模板前缀。缺少共享值时沿用 CID 格式化，CID 也为空时保留模板。旧模板 `enterprises` 为非零单段企业编号补上 `1.3.6.1.4.1.`，值为 `0` 时继续按原文替换。通用 `substitutions` 按变量原文替换。OID 的 `--key` 接受完整模板、旧模板及展开后的值。

其他 Book 在 `field_options.<列名>.substitutions` 声明 `{"{{model}}":"BMCSHELL_TARGET_MODEL"}`，即可在该文本列中采用对应共享字段的值。变量须已登记且为非敏感字段。规则适用于列表和正文，单次字面替换，普通检索使用库内原值。每个值最多 256 字节，最终单列上限 2 MiB。新元信息需要 dev.8 或更新核心。

## 依赖诊断与修复

框架内置一份修复建议。插件可在 `bmc-package.json` 中设置 `"repairSuggestions": "repair.json"`，并把该文件列入 `assets`。文件使用以下格式，`dependency` 对应插件已声明的 `systemDependencies[].id`。

```json
{
  "schema": 1,
  "suggestions": [
    {
      "dependency": "curl",
      "suggestion": "通过 MSYS2 安装 curl。",
      "platform": "windows-amd64",
      "manager": "msys2",
      "package": "msys/curl"
    }
  ]
}
```

`manager`、`package`、`platform` 均可省略以提供手动说明。当前自动安装支持 Windows x64 的 MSYS2 和 `msys/` 仓库包。诊断根据当前安装的插件读取两类建议，事务更新框架私有 SQLite 索引。`bmc doctor -d` 为缺失依赖显示建议和来源，`bmc doctor -a` 将同一包合并为一项，在可交互的 MSYS2 终端里由用户选择后交给包管理器确认安装。

## V2 工具和代理

通用工具仓库为 `bmcshell-tool-ipmi`、`bmcshell-tool-redfish` 和 `bmcshell-tool-snmp`。SBMC 工具仓库为 `sbmc-tool-cidtool`、`sbmc-tool-smash` 和 `sbmc-tool-bssh`，CID 关联信息库为 `sbmc-library-cidmap`。CIDTool 和 SMASH 依赖 IPMI。用 `bmc tool <工具名> --help` 查看包内帮助。

`bmc use <IP>` 通过通用解析器选定目标。设备离线时也可选择 IP。设备专属的 CID 识别和凭据匹配由对应解析器处理。用 `bmc status --env` 查看已登记的会话变量，敏感值会脱敏。切换目标或执行 `bmc exit` 时清除旧会话凭据。

`bmc proxy` 查看 SOCKS5 设置。`bmc proxy -m 127.0.0.1:1081` 修改地址并保留当前开关状态。`bmc proxy on` 启用代理并在当前 Bash 设置公共代理变量。`bmc proxy off` 恢复开启前的公共代理变量。执行 `bmc exit` 后当前 Bash 的代理继续生效。

设备解析、软件源和发布包下载遵循同一份代理设置。SSH 使用 TCP 转发，IPMI 和 SNMP 使用 UDP 转发。代理不可用或缺少所需能力时，相关操作报错并停止。

## 安装更新与恢复

解压发布包后，先在 Bash 中手动加载 `bin/bmcshell.sh`。要在 Git Bash 或 MSYS2 中自动加载，把同一个 `source` 命令写入 `~/.bashrc`，使用绝对路径，例如 `source '/d/Tools/BMCShell/bin/bmcshell.sh'`。如果登录型 Bash 的 `~/.bash_profile` 尚未加载 `~/.bashrc`，加入 `[[ ! -f ~/.bashrc ]] || source ~/.bashrc`。重开终端，用 `bmc version` 和 `bmc doctor -d` 确认版本和数据路径。

程序默认使用解压根目录作为数据目录。设置 `BMCSHELL_HOME` 时使用指定的数据目录。

用 `bmc update` 从官方发行版索引选择最新的 Windows x64 发布包，或用 `bmc update --version <版本>` 选择索引里的指定版本。下载安装前会比对索引和发布校验清单，再验证 ZIP。使用旧版布局的 dev.4 用户首次切换时，解压新版 ZIP。要迁移旧数据，关闭旧版终端后，将旧版 `bmc doctor -d` 显示的 `Data` 目录中的内容复制到新安装根目录或指定的 `BMCSHELL_HOME`。新版可读取旧布局，并在相关写入时登记到新版布局。回到 dev.4 时使用原安装和独立的旧版数据目录。

发行版索引记录 GitHub 已发布的框架版本。每个版本的 `channel` 为 `stable`（正式版）或 `dev`（开发版）。正式版的版本号没有预发行后缀，且 GitHub 未将其标为预发行版。`latestStable` 和 `latestDev` 分别指向两种类型中的最新版本，缺少对应类型时为 `null`。`latest` 是两种类型中版本号最高的版本。`releases` 按版本从新到旧排列，每个版本列出发布标签、时间、ZIP 和校验清单地址以及 SHA-256。索引由 GitHub Actions 刷新。[下载框架发行版索引](https://github.com/BMCShell/BMCShell/releases/download/release-index/index.json)

```powershell
$index = Invoke-RestMethod 'https://github.com/BMCShell/BMCShell/releases/download/release-index/index.json'
$index.releases[0].artifacts[0].url
$index.releases | Select-Object version, channel, publishedAt
```

离线更新使用 `bmc update --file <发布包>`。将发布 ZIP 和对应的 `.manifest.json` 文件放在同一目录。主程序校验 ZIP 后将新版写入独立的版本目录，核对暂存程序报告的版本，再切换 `bin/active`。重新打开 Bash 终端，沿用原来的加载路径即可进入所选版本。

### 启动和数据恢复

`bmc doctor -d` 会报告已登记但缺失或损坏的包。用原包执行 `bmc plugin install --file <路径>` 可修复同版本文件，也可用报告中的 UUID 卸载损坏的包。

- 启动器或 `bin/active` 损坏时，可在 PowerShell 运行仍可执行的便携版 EXE，例如 `& 'D:/Tools/BMCShell/bin/releases/3.0.0-dev.6/BMCShell.exe' update --file 'D:/Downloads/BMCShell-3.0.0-dev.6-windows-amd64.zip'`。ZIP 与校验清单放在同一个目录。
- 如果版本目录也损坏，关闭所有 BMCShell 终端和相关进程，核对 ZIP 的 SHA256 后解压到单独目录，将原安装的 `bin` 改为一个尚未使用的备份目录名，再把解压出来的 `BMCShell/bin` 放到原安装。新终端验证失败时恢复原 `bin`。
- 备份工作数据时，先用 `bmc doctor -d` 确认输出中的「数据目录」。关闭相关终端和进程后，一并备份整个数据目录。恢复先把备份复制到独立目录，用 `BMCSHELL_HOME` 指向该副本，检查 `bmc plugin list --installed` 和关键 Library 记录，再切换实际使用的配置。登记库和它引用的发布文件、工作库必须来自同一次备份。若 dev.6 在编辑 Library 时意外退出，回退到 dev.5 前先用 dev.6 打开该库，完成待同步审计。

### 历史版本维护

dev.7 在成功更新后记录可回退的上一版。维护保留当前核心和上一可用核心，保留插件的活动版、可回退版及仍被登记或依赖的发布文件。Library 保留当前工作库、发布基准、最近升级前的快照，以及已卸载库最后使用的工作库。历史关系无法确认的旧文件继续保留。

dev.7 或更新版本处于活动状态时，关闭使用这个安装和数据目录的所有 BMCShell 终端。从 PowerShell 7.4 运行发行包中的脚本，默认只列出本次可清理的路径。将下面的安装和备份路径替换成实际路径。独立数据目录可以用 `-DataDirectory` 指定，默认沿用 `BMCSHELL_HOME`。

```powershell
$installPath = 'D:/Tools/BMCShell'
$activeVersion = (Get-Content -LiteralPath (Join-Path $installPath 'bin/active') -Raw).Trim()
$historyScript = Join-Path $installPath "bin/releases/$activeVersion/scripts/maintain-history.ps1"
& $historyScript -InstallationDirectory $installPath
& $historyScript -InstallationDirectory $installPath -Apply -BackupDirectory 'E:/BMC-Backups/BMCShell-2026-11-11'
```

`-Apply` 在新建的外部目录保存完整副本，核对文件哈希，并用副本里的程序验证登记表和 SQLite Library。通过后仅清理已确认且超出保留范围的文件。备份包含业务数据，按原数据的访问权限保存。恢复时可从备份副本独立启动，分离的数据目录继续用 `BMCSHELL_HOME` 指定。

## 运行端到端验证

在 UCRT64 中分别构建主程序和脚手架后，从主仓库执行以下命令。默认使用同级 `BMCShell-Scaffold` 仓库的 EXE，也可以用 `BMCSHELL_SCAFFOLD` 指定路径。测试通过脚手架生成五种临时扩展包，使用临时配置目录和示例 IP。

```bash
BMCSHELL_HOME="$(cygpath -w "$(mktemp -d)")" bash tests/smoke.sh
```

同级仓库包含实际 Tool、Book 和 CIDmap 插件时，可用 `BMCSHELL_TEST_ROOT` 指定一个新建的空目录，再执行 `bash tests/smoke-v2-tools.sh`。此测试用模拟命令验证认证、CID 和代理参数。

打包后在 PowerShell 用上一份可用便携版 ZIP 做发布验收。将两个 Bash 路径替换为本机路径。

```powershell
./tests/smoke-release.ps1 -Archive ./dist/BMCShell-3.0.0-dev.9-windows-amd64.zip -PreviousArchive ./dist/BMCShell-3.0.0-dev.8-windows-amd64.zip -GitBash 'C:/Environment/Git/bin/bash.exe' -Msys2Bash 'C:/Environment/msys64/usr/bin/bash.exe'
```

# BMCShell 扩展命名与脚手架参考

## 包名与清单字段

完整包名由 `publisher-类型前缀-shortName` 组成，源码仓库根目录名与 `name` 完全一致。借鉴 npm 将机器包名与描述分开的思路，完整包名采用单段目录可用的格式。[npm 包清单](https://docs.npmjs.com/cli/v11/configuring-npm/package-json/#name)

| 清单字段 | 规则 | 示例 |
| --- | --- | --- |
| `id` | 扩展的永久 UUID，创建时生成。改名、发布新版本或迁移仓库时沿用。 | `15b746d7-7b0d-4556-89c8-d88078a3117a` |
| `publisher` | 单段小写英文字母和数字，以字母开头。 | `bmcshell`、`sbmc` |
| `type` | 清单中的扩展类型。命令行使用 `tool`，清单写 `toolplugin`，包名前缀为 `tool`。其余前缀和类型一致。 | `book`、`library`、`theme`、`toolplugin`、`bundle` |
| `shortName` | 功能短名，1–48 个字符，同一发布方与类型内唯一。 | `redfish`、`redfish-accounts`、`cid-map` |
| `name` | 完整英文包名，用于命令行、源码目录和发布文件。 | `sbmc-book-redfish-accounts` |
| `displayName` | 面向用户的显示名，支持中文。 | `Redfish 账户工具书` |
| `description` | 用途说明，支持中文。 | `查询账户接口与请求示例。` |
| `version` | 包版本，脚手架默认 `1.0.0`。 | `1.0.0`、`1.1.0-rc.1` |

`shortName` 的规则为 `^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`。以小写英文字母开头，以字母或数字结尾。多个词用单个短横线连接。`snmp-v3` 可表达协议版本，软件包自身的发布版本单独写入 `version`。

同一个 `shortName` 可用于不同类型。`sbmc-book-redfish` 表示手册，`sbmc-tool-redfish` 表示工具。通过完整 `name` 拆分时，第一段是发布方，第二段是类型前缀，剩余部分整体作为 `shortName`。

## 创建方式

```text
BMCShell-Scaffold.exe create
  [--publisher <发布方> --type <book|library|theme|tool|bundle> --short-name <短名>
   | --name <完整包名>]
  [--display-name <显示名>] [--description <描述>] [--version <版本>]
  [-o <父目录> | --output <父目录>] [--member <UUID> ...]
```

| 方式 | 行为 |
| --- | --- |
| 交互式 | 运行 `create`。向导收集发布方、类型、短名、显示名和描述，预览完整包名及目标路径，确认后创建。只传 `-o` 时也进入向导并沿用指定父目录。 |
| 按字段创建 | 同时提供 `--publisher`、`--type` 和 `--short-name`，框架计算完整包名。 |
| 按包名创建 | 提供 `--name`，框架拆分并校验发布方、类型和短名。完整包名模式与分字段模式互斥。 |
| Bundle 成员 | 创建 Bundle 时至少提供一个 `--member <UUID>`，可重复指定。交互模式在选中 Bundle 后收集成员。 |

直接创建时，省略 `--display-name` 使用完整包名作为显示名，省略 `--description` 时生成空描述供后续填写。项目包含独立 Git 仓库、包清单、README、帮助、变更历史、打包脚本和类型对应的模板。Book 模板生成使用 v2 数据结构的 `*.book.db`。

### 输出路径

`-o` 和 `--output` 指定父目录，默认当前目录。实际创建位置为 `<父目录>/<name>/`，目标已存在时报告冲突。父目录需已存在且可写。

PowerShell 示例，输出到 `D:\bmcshell-book-redfish\`。

```powershell
.\BMCShell-Scaffold.exe create --name bmcshell-book-redfish -o 'D:\' `
  --display-name 'Redfish 工具书' --description '查询 Redfish 接口与请求示例'
```

在 WSL 中调用 Windows 版 EXE 时，用 `wslpath` 转换父目录，生成的项目从 WSL 访问为 `/mnt/d/bmcshell-book-redfish/`。[WSL 路径转换](https://learn.microsoft.com/en-us/windows/dev-environment/wsl-interop#path-translation)

```bash
./BMCShell-Scaffold.exe create --name bmcshell-book-redfish -o "$(wslpath -w /mnt/d)"
```

## 构建、发布与安装

- 正式构建及打包入口校验清单字段、推导出的 `name` 和源码项目根目录名。编译型工具在编译前检查，数据型包在打包前检查。安装后的存储路径使用 UUID。
- 发布归档为 `<name>@<version>.zip`，SHA256 清单为 `<name>@<version>.manifest.json`。有多个平台产物时用平台子目录区分，清单记录对应平台和文件路径。
- `install`、`upgrade`、`show` 和 `remove` 接受完整 `name` 或 UUID。存在多个同名候选时，交互终端列出类型、来源、版本和 UUID 供选择，脚本可直接用 UUID。用 `--file` 安装本地目录或 ZIP。[软件源管理](SOFTWARE-SOURCES.md)
- 依赖、锁定、升级和回退以 UUID 为准。迁移旧包时保留 UUID，将原中文名称放入 `displayName`，旧 `summary` 可迁移到 `description`。

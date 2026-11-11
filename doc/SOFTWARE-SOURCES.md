# BMCShell 软件源管理

软件源提供一个可通过 HTTPS 获取的 JSON 索引。索引列出包的 UUID、名称、版本、适用平台、下载地址、SHA256 和依赖。更新索引和安装包分开执行。某个源刷新失败时，框架继续保留该源上次验证通过的索引。

## 用户端

使用 `bmc setting` 管理软件源。设置名为 `plugin.source.<名称>`，保留名称 `official` 用于官方源。示例地址请替换为维护者提供的索引地址。

```text
bmc setting --set plugin.source.official https://packages.example.org/bmcshell/index.json
bmc setting --set plugin.source.internal https://packages.example.com/bmcshell/index.json
bmc plugin update
bmc plugin list
bmc plugin list --installed
bmc plugin list --installed --detial
bmc plugin search redfish
bmc plugin install bmcshell-tool-ipmi bmcshell-tool-redfish@1.0.1
bmc plugin upgrade bmcshell-tool-redfish
bmc setting --reset plugin.source.internal
```

`bmc plugin list` 读取当前配置的软件源索引缓存，列出各源的全部包版本，包括其他平台或 Core API 的包。已安装版本显示 `[已安装]`，清单也包括仅存在于本地的已安装包。用 `bmc plugin update` 获取或刷新索引。

`bmc plugin list --installed` 或 `bmc plugin list -i` 只查看已安装的包。两种清单均可按包类型筛选，例如 `bmc plugin list tool` 或 `bmc plugin list --installed tool`。`bmc plugin search` 按关键词查询，每个 UUID 显示一个最新的兼容版本。

加 `-d` 或 `--detial` 显示包描述，可与已安装筛选和类型筛选组合。旧索引可能没有描述字段，此时已安装版本可读取本地包清单。缺少描述的条目显示 `-`。源维护者可重新生成索引，客户端随后刷新缓存以获取描述。

安装和升级使用完整包名。一次安装可传入多个包名，每个包都可用 `@version` 指定版本。若名称相同的包具有不同 UUID，安装时在交互终端列出候选供用户选择。已安装包升级时沿用安装来源。框架按平台和依赖约束解析版本，并校验下载 ZIP 的 SHA256 与包内清单。

本地安装使用一个 `--file`，后接一个或多个目录或 ZIP 路径。软件源安装和本地安装分别执行。一批包共同校验，校验通过后统一启用。

```bash
bmc plugin install --file './packages/book.zip' './packages/internal tool'
```

默认校验 HTTPS 证书。使用 RFC 1918 私有 IPv4 或 IPv6 ULA 地址配置内网源时，可以显式添加 `--insecure`。它只影响该源配置地址的同一 HTTPS IP 和端口，跨源下载和重定向继续校验证书。ZIP 的 SHA256 和包内清单仍会核对。

```bash
bmc setting --set plugin.source.internal 'https://10.0.0.10:10100/s/<索引令牌>/download' --insecure
bmc plugin update
```

用相同 URL 重新设置且省略 `--insecure`，即可恢复严格校验。切换选项后需要刷新索引。

Library 数据随发布包安装或升级，并按工作库、旧版基准和新版发布数据进行差异合并。主程序更新默认读取主仓 GitHub Releases。[Library 发布与升级](LIBRARY-PACKAGE.md)

## 维护公开源

建议把 ZIP 放在各扩展仓库的 GitHub Releases，再用独立的索引仓库发布 `index.json`。ZIP 使用具体 tag 的资产下载地址。索引保留仍可下载的历史版本，供框架选出相互兼容的组合。索引可以用 GitHub Pages 提供稳定地址。资产 URL 从已发布的 Release 复制，GitHub 可能调整包含特殊字符的资产文件名。[GitHub Releases](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases) · [Release 资产接口](https://docs.github.com/en/rest/releases/assets) · [GitHub Pages](https://docs.github.com/en/pages/getting-started-with-github-pages/creating-a-github-pages-site)

发版时先构建和打包扩展，上传 ZIP，再生成索引。准备一份 `artifacts.json`，路径以这份文件所在目录为基准。

```json
[
  {
    "path": "dist/sbmc-library-cid@2.0.0.zip",
    "url": "https://github.com/OWNER/REPO/releases/download/v2.0.0/RELEASE-ASSET.zip"
  }
]
```

```powershell
pwsh -File ./scripts/build-source-index.ps1 -ArtifactsFile ./artifacts.json -Output ./index.json -Merge
```

脚本从 ZIP 读取包清单并计算 SHA256，同时写入描述、依赖、Library 兼容范围和迁移信息。`-Merge` 保留现有索引条目。同一 UUID、版本和平台的 ZIP 内容变化时，需发布新版本。审核索引差异并确认下载链接可用后，再发布新索引。这个流程可以放进 CI。

## 其他部署方式

| 方式 | 适用场景 |
| --- | --- |
| GitHub Releases 同时存放索引与 ZIP | 包较少，想只维护一个入口。可给索引单独设发布仓库，通过最新索引发行版的固定资产名提供入口。ZIP 条目继续引用具体 tag。 |
| 对象存储或静态 HTTPS 服务 | 想用自己的域名，或让 CI 把 ZIP 和索引上传到同一个位置。可以在索引里用相对下载路径。 |
| 公司内网 HTTPS | 发布内部工具包或 Library 模板。内网软件源存放可安装的包，管理员密码和 SNMP 团体名等运行数据继续走独立私有来源。 |

GitHub 的最新发行版资产入口形如 `/releases/latest/download/index.json`。[GitHub 发行版链接](https://docs.github.com/en/repositories/releasing-projects-on-github/linking-to-releases)

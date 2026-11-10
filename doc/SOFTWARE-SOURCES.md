# BMCShell 软件源管理

软件源提供一个可通过 HTTPS 获取的 JSON 索引。索引列出包的 UUID、名称、版本、适用平台、下载地址、SHA256 和依赖。更新索引和安装包分开执行。某个源刷新失败时，框架继续保留该源上次验证通过的索引。

## 用户端

将命令中的示例域名替换为你维护的索引地址。官方源名固定为 `official`，地址由维护者配置。自定义源可使用独立的名称。

```text
bmc plugin source set-official https://packages.example.org/bmcshell/index.json
bmc plugin source add internal https://packages.example.com/bmcshell/index.json
bmc plugin source list
bmc plugin update
bmc plugin update internal
bmc plugin install sbmc-library-cid --source internal
bmc plugin upgrade sbmc-library-cid
bmc plugin upgrade
bmc plugin source remove internal
```

`install` 和 `upgrade` 都可以用包名或 UUID。无参数的 `upgrade` 会列出可升级项供选择，再确认最终计划。同一 UUID 出现在多个源时，用 `--source` 选定目标来源；已从软件源安装的包默认沿用其来源。框架按平台和依赖约束选择版本，并校验下载 ZIP 的 SHA256 与包内清单。也可以用 `bmc plugin install --file <目录或 ZIP>` 安装本地包。

Library 的数据共享地址独立配置。发现远端快照的结构变化后，`bmc library refresh` 使用缓存的索引选择匹配结构的库和工具版本。确认前会列出版本、来源和数据差异。通过 `--dry-run` 可以预览，`--upgrade-with` 可用于手工提供本地发布包。[Library 发布与同步](LIBRARY-PACKAGE.md)

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

脚本从 ZIP 读取包清单并计算 SHA256，同时写入依赖、Library 兼容范围和迁移信息。`-Merge` 保留现有索引条目。同一 UUID、版本和平台的 ZIP 内容变化时，需发布新版本。审核索引差异并确认下载链接可用后，再发布新索引。这个流程可以放进 CI。

## 其他部署方式

| 方式 | 适用场景 |
| --- | --- |
| GitHub Releases 同时存放索引与 ZIP | 包较少，想只维护一个入口。可给索引单独设发布仓库，通过最新索引发行版的固定资产名提供入口。ZIP 条目继续引用具体 tag。 |
| 对象存储或静态 HTTPS 服务 | 想用自己的域名，或让 CI 把 ZIP 和索引上传到同一个位置。可以在索引里用相对下载路径。 |
| 公司内网 HTTPS | 发布内部工具包或 Library 模板。内网软件源存放可安装的包，管理员密码和 SNMP 团体名等运行数据继续走独立私有来源。 |

GitHub 的最新发行版资产入口形如 `/releases/latest/download/index.json`。[GitHub 发行版链接](https://docs.github.com/en/repositories/releasing-projects-on-github/linking-to-releases)

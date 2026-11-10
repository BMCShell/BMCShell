# BMCShell Library 发布与同步参考

## 发布包与数据来源

Library 使用 SQLite 保存表结构和数据。包清单使用 JSON 声明库的用途、版本和同步规则。JSON 也可用作人工导入与导出的交换格式。

```text
sbmc-library-cid@1.0.0.zip
├── bmc-package.json
├── README.md
├── help.json
├── changelog.json
└── library/
    └── cid.library.db
```

包内的 `*.library.db` 是建库模板，可以是空库或带示例数据的库。数据库本身保存表定义，`_bmcshell_library` 记录包 UUID、结构和数据版本。清单的 `data` 指向数据库，`library` 声明结构版本、同步表及各表的唯一键。CID 定制信息可以用 `cid` 作键。同一 CID 对应多条记录时，应选用稳定的记录 ID 或非敏感复合键。

```json
{
  "data": "library/cid.library.db",
  "library": {
    "schemaVersion": 1,
    "tables": [{"name": "records", "keys": ["key"]}],
    "lookup": {"table": "records", "value": "value"}
  }
}
```

`bmc library query <ID-or-name> <key...>` 按 `lookup` 对应表的键顺序查询。已有的 JSON Library 包仍可以查询。

通过 `bmc library source <ID-or-name> <URL>` 将共享来源登记到私有设置。使用 `--clear` 删除地址。一个 GET URL 对应一份独立、全量的 SQLite 快照。公司端可用 SQLite 的备份接口或 `VACUUM INTO` 生成快照。公开包使用可发布的示例数据，管理员密码和 SNMP 团体名走独立的私有凭据来源。[SQLite 快照备份](https://www.sqlite.org/backup.html) · [VACUUM INTO](https://www.sqlite.org/lang_vacuum.html#vacuum_with_an_into_clause)

| 版本 | 含义 |
| --- | --- |
| `version` | Library 包版本，随代码、帮助和数据库模板发布。 |
| `schemaVersion` | 表结构和查询接口的兼容版本。结构变更时随包提供对应的迁移规则。 |
| `dataVersion` | 数据快照的版本。结构相同的记录更新可单独发布。 |

## 常规数据同步

`bmc library refresh <ID-or-name>` 下载快照到暂存目录，以只读方式打开并校验库标识、结构版本、表、列和唯一键。一次同步使用本机工作库、远端快照和上次处理过的远端基线，按唯一键比较记录。首次同步用包内模板作为初始基线。

| 比较结果 | 处理 |
| --- | --- |
| 内容相同 | 跳过。 |
| 远端新增或修改 | 列入预览，可展开字段差异。 |
| 只有本地修改 | 保留本地。 |
| 双方修改同一记录，结果不同 | 显示冲突，用户选择保留本地、采用远端或取消。 |
| 远端完整快照缺少旧记录 | 单独列出建议删除的记录并确认。 |

用户确认后复核本机库版本，将数据和比较基线一起提交。`--dry-run` 只预览。`--replace-all` 仅用于结构兼容且校验通过的完整快照，需要二次确认，并保留回退快照。

## 表结构变化与联合升级

发现结构版本或实际表结构不一致时，暂停同步。提示当前版本、远端版本、变更的表和列，以及受影响的查询工具。比较依据为表名、列、类型、主键、唯一键和约束等结构信息。结构版本相同但内容定义不一致时，提示发布源检查版本声明与快照。

框架通过缓存的软件源索引为当前 Library 和已安装的依赖工具寻找兼容版本，列出候选版本和来源。每次联合升级处理一个 Library 的结构。用户可以查看差异、预览联合升级计划或取消。用 `bmc plugin update` 刷新索引，用 `--source` 指定目标 Library 的软件源。也可以通过 `--upgrade-with` 重复指定已经取得的新版 Library 和工具 ZIP 或源码目录。使用 `--dry-run` 只预览计划。[软件源管理](SOFTWARE-SOURCES.md)

```text
bmc plugin update
bmc library refresh sbmc-library-cid --source internal --dry-run
bmc library refresh sbmc-library-cid --source internal
```

新版 Library 的 `libraryMigrations` 按旧结构版本指定包内 SQL，例如 `{"1":"library/migrate-from-1.sql"}`。工具的 `librarySchemas` 以 Library UUID 为键声明兼容范围，例如 `{"<Library UUID>":{"min":2,"max":2}}`。

1. **计算兼容组合。** 查询缓存索引或校验指定的本地发布包，选择满足迁移规则和工具依赖的版本，并列出每个组件的当前版本和目标版本。软件源包下载后要通过 SHA256 和清单校验。
2. **暂存并校验。** 在副本中迁移当前工作库和比较基线。按新版包的查询定义和表结构校验迁移结果，再处理与共享快照的数据差异。
3. **确认后一起切换。** 复核活动包版本、工作库和比较基线，准备新版数据库快照。最后切换包集合，当前 Library 版本对应的工作库与比较基线一同生效。
4. **保留整组回退点。** 用户取消、缺少兼容版本、迁移失败或检测到并发修改时，保留原有组合。用 `bmc library rollback <ID-or-name>` 回退最近一次匹配的联合升级。发生其他包变更后，框架会拒绝回退，避免覆盖这些变更。

例如，本机工具只支持结构 `1`，而公司发布了结构 `2` 的快照。界面会先列出支持结构 `2` 的工具和 Library 版本。用户确认后，将工具更新与数据库迁移放在同一升级计划中。

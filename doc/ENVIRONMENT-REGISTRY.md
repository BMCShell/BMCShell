# BMCShell 3.0 环境变量管理

框架加载 [环境变量注册表 CSV](ENVIRONMENT-REGISTRY-v3.csv) 和已安装插件的变量注册表，在内存中维护统一索引。优先加载 BMCShell 数据目录（可由 `BMCSHELL_HOME` 指定）下的 `ENVIRONMENT-REGISTRY-v3.csv`，其次加载程序随附的 `doc/ENVIRONMENT-REGISTRY-v3.csv`，最后使用编译进程序的注册表。编辑后的文件在下次相关命令执行时加载。CSV 格式、名称、声明冲突或默认值校验失败时，该操作会报错。

## 命名和归属

新变量采用 `发布者_分组_名称`。统一使用大写 ASCII。发布者和分组各用一个以字母开头的代码。分组概念含多个单词时，选用简短代码，完整含义写入注册表说明。前两个下划线划出发布者和分组，余下部分是字段名，例如 `SBMC_PLATFORM_PROJECT_NAME` 的分组是 `PLATFORM`，字段名是 `PROJECT_NAME`。发布者段对应扩展清单中的发布者，`BMCSHELL` 前缀归框架使用。变量名是注册表和插件清单的匹配键。CSV 中的逻辑字段及现有载体用于记录迁移关系。

框架保留 `BMCSHELL_CONTEXT`、`BMCSHELL_ENTRY` 等现有兼容和启动接口。`BMCSHELL_BRIDGE_` 前缀归终端桥接内部使用。CSV 在现有载体列记录兼容接口。

框架 CSV 的 OEM 示例只供参考。插件要登记自己的字段，在包清单声明 `publisher` 和 `envRegistry`。`envRegistry` 指向包内 CSV，推荐使用 `name,key,sensitive,default` 表头。以下是发布者 `sbmc` 的注册表示例。

```csv
name,key,sensitive,default
SBMC_PLATFORM_CID,oem.sbmc.cid,false,
SBMC_PLATFORM_PROJECT_NAME,oem.sbmc.project_name,false,
SBMC_PLATFORM_CHIP,oem.sbmc.chip,false,
```

插件登记的变量必须使用自己的发布者前缀。逻辑键可以留空，也可以填写 `oem.<发布者>.<字段>`。同一变量名或逻辑键由一个包登记。框架 CSV 可修改默认值、添加框架字段，内置字段的逻辑键和敏感标记固定。

插件直接调用第三方工具的原生命令和选项。插件把框架字段映射为第三方命令所需的参数。例如 IPMI 插件把目标 IP 和用户名传给 `ipmitool` 的 `-H` 和 `-U`。需要用 `-E` 认证时，插件只为这次调用设置 ipmitool 原生的 `IPMI_PASSWORD`。第三方原生变量由调用它的插件处理，框架注册表和插件环境变量清单只管理 BMCShell 侧的字段。

## 值的优先级

从高到低逐层查找首个显式设置的值。

| 优先级 | 来源 | 说明 |
| --- | --- | --- |
| 1 | 运行时 | 当前会话值，包括 `bmc use` 提交的目标及受管会话状态。框架忽略父进程环境里任意同名变量。 |
| 2 | 安装时导入的默认配置清单 | 用 `bmc setting --import-env defaults.json` 导入，文件内容为变量名到字符串值的 JSON 对象。 |
| 3 | 框架注册表默认值 | 来自框架 CSV 的默认值列。 |
| 4 | 插件注册表默认值 | 由插件随包声明。同一变量的插件层默认值以登记该变量的插件为准，避免安装顺序改变结果。 |

注册表默认值列留空表示未提供默认值。安装配置例如 `{"BMCSHELL_TLS_MODE":"verify"}`。运行时或安装配置显式设置的空字符串仍是一个值，按字段约束校验。必选依赖在四层查找后仍缺值，或选中的值未通过校验时，框架报出变量名并停止启动工具。可选依赖缺值时保持未设置。密码和 SNMP 团体名的登记项只包含元数据。现有凭据来自 Bash 的私有会话状态，仅交给声明读取相应字段的工具。

## 插件清单和工具执行

插件包声明它登记的变量及默认值。工具入口分别用 `envWrites` 声明可能创建或修改的变量，用 `envReads` 声明依赖的变量及是否必选。

```json
{
  "envWrites": [
    {"name": "SBMC_PLATFORM_CID"},
    {"name": "SBMC_PLATFORM_PROJECT_NAME"},
    {"name": "SBMC_PLATFORM_CHIP"}
  ],
  "envReads": [
    {"name": "BMCSHELL_TARGET_IP", "required": true},
    {"name": "BMCSHELL_TARGET_MODEL", "required": false}
  ]
}
```

工具清单只能引用已登记的变量。工具可以在活动会话中写入本包登记、已声明写入的非敏感字段。工具在子进程中设置的环境变量只影响自己和它启动的子进程。需要把结果留给后续工具时，将变量名到字符串值的 JSON 对象写入 `BMCSHELL_RESULT_FILE` 指定的文件，例如 `{"SBMC_PLATFORM_CID":"example"}`。工具成功退出后，框架校验结果并提交到当前会话。切换目标成功或执行 `bmc exit` 后，清除前一个目标的会话值。

框架启动 EXE 或 Bash 工具子进程前，清理继承环境中同名的受管会话变量，再按工具读清单注入最终值。EXE 使用标准环境变量读取接口，Bash 使用带引号的变量展开。当前 V2 同 Shell 工具由兼容层适配。解析器返回候选会话的现有 JSON 文件协议独立于工具的取值接口。


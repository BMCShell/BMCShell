package cli

import "bmcshell/internal/render"

const useUsage = "bmc use <IP> [--resolver <解析器>] [-- <解析器参数>...]"

var builtinHelp = map[string]render.Topic{
	"help": {
		Summary: "查看框架命令、工具书、业务库和扩展工具的帮助。",
		Usage: []string{
			"bmc help",
			"bmc help <命令>",
			"bmc <命令> -h|--help",
			"bmc help plugin <操作>",
			"bmc help book <书名>",
			"bmc help library <库名>",
			"bmc help tool <工具名>",
			"bmc help use <解析器>",
		},
		Examples: []render.Example{
			{Command: "bmc help status", Description: "查看目标状态命令的帮助。"},
			{Command: "bmc plugin install --help", Description: "查看扩展包安装方式。"},
		},
	},
	"book": {
		Summary: "列出已安装的工具书，离线查询命令和接口资料。",
		Usage: []string{
			"bmc book [-l|--list]",
			"bmc book <书名> [查询参数...]",
			"bmc book <书名> -h|--help",
			"bmc book <书名> -v|--version",
		},
		Options: []render.Option{
			{Name: "-l, --list", Description: "列出已安装的工具书。", Default: "不指定书名时列出全部工具书"},
			{Name: "-h, --help", Description: "显示指定工具书支持的查询方式。"},
			{Name: "-v, --version", Description: "显示指定工具书的版本。"},
		},
		Examples: []render.Example{
			{Command: "bmc book", Description: "查看已安装的工具书。"},
			{Command: "bmc book redfish -s SessionService", Description: "安装 Redfish 工具书后，搜索会话服务接口。"},
		},
	},
	"doctor": {
		Summary: "检查框架、已安装扩展包和外部命令依赖。",
		Usage:   []string{"bmc doctor [-d|--detail] [-f|--filter <ok|missing>] [-a|--auto]"},
		Options: []render.Option{
			{Name: "-d, --detail", Description: "显示版本、数据目录、依赖路径及修复建议。"},
			{Name: "-f, --filter <ok|missing>", Description: "筛选正常项（ok）或缺失和损坏项（missing）。", Default: "显示所有检查结果"},
			{Name: "-a, --auto", Description: "在交互式 MSYS2 Bash 中选择并安装支持自动修复的依赖。"},
		},
		Examples: []render.Example{
			{Command: "bmc doctor --detail --filter missing", Description: "查看缺失依赖及修复建议。"},
		},
		Notes: []string{"依赖检查确认命令能否在 PATH 中找到。详细输出列出包声明的版本和功能要求。"},
	},
	"exit": {
		Summary: "结束当前 Bash 的 BMC 会话，清除目标和会话凭据。",
		Usage:   []string{"bmc exit"},
		Examples: []render.Example{
			{Command: "bmc exit", Description: "退出设备会话并恢复终端提示符。"},
		},
	},
	"library": {
		Summary: "列出已安装的业务库，查看库信息并管理记录。",
		Usage: []string{
			"bmc library [-l|--list]",
			"bmc library <库名>",
			"bmc library <库名> -h|--help",
		},
		Options: []render.Option{
			{Name: "-l, --list", Description: "列出已安装的业务库。", Default: "不指定库名时列出全部业务库"},
			{Name: "-h, --help", Description: "显示指定业务库的帮助。"},
		},
		Examples: []render.Example{
			{Command: "bmc library -l", Description: "查看可用库名及版本。"},
			{Command: "bmc library cidmap", Description: "安装 CIDmap 后，打开该库。"},
		},
		Notes: []string{"交互终端可通过 SQLite 业务库的菜单查看、新增、修改和删除记录。"},
	},
	"plugin": {
		Summary: "查看软件源索引，查找、安装、升级和卸载扩展包。",
		Usage: []string{
			"bmc plugin list [-i|--installed] [-d|--detail] [类型]",
			"bmc plugin update",
			"bmc plugin search <关键词> [-d|--detail]",
			"bmc plugin install <包名[@版本]> [<包名[@版本]>...]",
			"bmc plugin install --file <发布包路径> [<发布包路径>...]",
			"bmc plugin upgrade [包名或 UUID]",
			"bmc plugin remove <包名或 UUID>",
			"bmc plugin <操作> -h|--help",
			"bmc plugin help",
		},
		Examples: []render.Example{
			{Command: "bmc plugin list --installed", Description: "查看本机已安装的扩展包。"},
			{Command: "bmc plugin install --help", Description: "查看安装方式、版本选择和示例。"},
		},
	},
	"plugin list": {
		Summary: "查看软件源索引和已安装的扩展包。",
		Usage:   []string{"bmc plugin list [-i|--installed] [-d|--detail] [类型]"},
		Options: []render.Option{
			{Name: "-i, --installed", Description: "只列出本机已安装的包。", Default: "列出缓存索引和本机已安装的包"},
			{Name: "-d, --detail", Description: "以表格显示包类型、来源、核心 API、平台、UUID 和描述。"},
			{Name: "<类型>", Description: "筛选 book、library、theme、tool 或 bundle。"},
		},
		Examples: []render.Example{
			{Command: "bmc plugin list --installed --detail tool", Description: "以表格查看已安装的工具扩展包。"},
		},
	},
	"plugin update": {
		Summary: "从已配置的软件源刷新本地扩展包索引。",
		Usage:   []string{"bmc plugin update"},
		Examples: []render.Example{
			{Command: "bmc plugin update", Description: "刷新索引后即可搜索或升级扩展包。"},
		},
		Notes: []string{"使用 bmc setting 查看或配置软件源。每次联网请求最多等待 5 分钟，遇到临时网络错误最多重试两次。刷新失败时保留上次有效的索引。进度写入标准错误。"},
	},
	"plugin search": {
		Summary: "在缓存索引中按包名搜索扩展包。",
		Usage:   []string{"bmc plugin search <关键词> [-d|--detail]"},
		Options: []render.Option{
			{Name: "<关键词>", Description: "按包名做不区分大小写的子串匹配。每个包显示最新兼容版本。"},
			{Name: "-d, --detail", Description: "以表格显示匹配包的完整信息。"},
		},
		Examples: []render.Example{
			{Command: "bmc plugin search redfish", Description: "查找包名包含 redfish 的扩展包。"},
			{Command: "bmc plugin search redfish --detail", Description: "以表格查看匹配的扩展包。"},
		},
	},
	"plugin install": {
		Summary: "从软件源、本地 ZIP 或本地包目录安装扩展包。",
		Usage: []string{
			"bmc plugin install <包名[@版本]> [<包名[@版本]>...]",
			"bmc plugin install --file <发布包路径> [<发布包路径>...]",
		},
		Options: []render.Option{
			{Name: "<包名[@版本]>", Description: "使用索引中的完整包名，附加 @版本 可指定发布版本。", Default: "选择兼容的最新版本"},
			{Name: "--file <发布包路径>", Description: "指定 ZIP 或包含 bmc-package.json 的扩展包目录。多个路径以空格分隔，含空格的路径请加引号。"},
		},
		Examples: []render.Example{
			{Command: "bmc plugin install sbmc-book-redfish", Description: "在软件源提供此包时，安装 Redfish 工具书。"},
			{Command: "bmc plugin install --file './sbmc-book-redfish@1.0.0.zip'", Description: "从当前目录的发布 ZIP 安装。"},
			{Command: "bmc plugin install --file .", Description: "在扩展包目录中安装当前包。"},
		},
		Notes: []string{"从软件源安装前可运行 bmc plugin update 刷新索引。每次下载请求最多等待 5 分钟，遇到临时网络错误最多重试两次。安装进度写入标准错误。"},
	},
	"plugin upgrade": {
		Summary: "从软件源升级已安装的扩展包。",
		Usage:   []string{"bmc plugin upgrade [包名或 UUID]"},
		Options: []render.Option{
			{Name: "<包名或 UUID>", Description: "仅升级指定的包。", Default: "检查所有已安装包"},
		},
		Examples: []render.Example{
			{Command: "bmc plugin upgrade sbmc-book-redfish", Description: "升级已安装的 Redfish 工具书。"},
		},
		Notes: []string{"交互终端会先显示升级计划，确认后应用。每次下载请求最多等待 5 分钟，遇到临时网络错误最多重试两次。升级进度写入标准错误。"},
	},
	"plugin remove": {
		Summary: "按包名或 UUID 卸载已安装的扩展包。",
		Usage:   []string{"bmc plugin remove <包名或 UUID>"},
		Examples: []render.Example{
			{Command: "bmc plugin remove sbmc-book-redfish", Description: "卸载已安装的 Redfish 工具书。"},
		},
		Notes: []string{"卸载当前主题或默认解析器前，先通过 bmc setting 更改相关设置。"},
	},
	"proxy": {
		Summary: "查看或设置当前终端和 BMCShell 的 SOCKS5 代理。",
		Usage:   []string{"bmc proxy", "bmc proxy on|off", "bmc proxy -m 127.0.0.1:<端口>"},
		Options: []render.Option{
			{Name: "on, off", Description: "使用已配置的地址开启代理，或关闭代理。"},
			{Name: "-m 127.0.0.1:<端口>", Description: "保存本机 SOCKS5 地址。端口范围为 1–65535。代理已开启时立即切换地址。", Default: "127.0.0.1:1080"},
		},
		Examples: []render.Example{
			{Command: "bmc proxy", Description: "查看代理状态和地址。"},
			{Command: "bmc proxy -m 127.0.0.1:1081", Description: "将代理端口设为 1081。"},
		},
	},
	"setting": {
		Summary: "查看、修改、重置和检查框架与扩展设置。",
		Usage: []string{
			"bmc setting",
			"bmc setting --get <设置名>",
			"bmc setting --set <设置名> <值>",
			"bmc setting --set plugin.source.<名称> <软件源 HTTPS URL> [--insecure]",
			"bmc setting --reset [设置名]",
			"bmc setting --check",
		},
		Options: []render.Option{
			{Name: "--get <设置名>", Description: "输出当前值。"},
			{Name: "--set <设置名> <值>", Description: "保存指定设置。交互终端中，bmc setting 也会打开设置菜单。"},
			{Name: "--reset [设置名]", Description: "恢复单项默认值，或移除指定软件源。省略设置名会清除框架与扩展设置及已配置的软件源。"},
			{Name: "--check", Description: "检查已保存设置的有效性。"},
			{Name: "--insecure", Description: "仅在配置支持该选项的私有 HTTPS 软件源时跳过证书校验。", Default: "校验证书"},
		},
		Examples: []render.Example{
			{Command: "bmc setting --get use.default_resolver", Description: "查看默认解析器。"},
			{Command: "bmc setting --check", Description: "检查设置是否有效。"},
		},
	},
	"status": {
		Summary: "查看当前目标 IP 和解析器提供的设备信息。",
		Usage:   []string{"bmc status [--env]"},
		Options: []render.Option{
			{Name: "--env", Description: "查看已登记的会话环境变量，敏感值会被遮蔽。", Default: "显示目标 IP 和设备信息"},
		},
		Examples: []render.Example{
			{Command: "bmc status", Description: "查看当前目标。"},
			{Command: "bmc status --env", Description: "查看当前会话环境。"},
		},
	},
	"tool": {
		Summary: "列出扩展工具和解析器，查看可用状态、来源和帮助。",
		Usage: []string{
			"bmc tool [-l|--list]",
			"bmc tool -a|--all",
			"bmc tool <工具名> [-h|--help]",
			"bmc tool use@<解析器> [-h|--help]",
		},
		Options: []render.Option{
			{Name: "-l, --list", Description: "列出当前可用的工具和解析器。", Default: "仅显示当前可用的入口"},
			{Name: "-a, --all", Description: "同时显示暂不可用的入口及原因。"},
			{Name: "-h, --help", Description: "显示指定工具或解析器的帮助。"},
		},
		Examples: []render.Example{
			{Command: "bmc tool --all", Description: "查看工具可用状态和缺失依赖。"},
			{Command: "bmc tool ipmitool --help", Description: "安装 IPMI 工具后，查看其使用说明。"},
		},
	},
	"update": {
		Summary: "检查并更新 BMCShell 框架。",
		Usage: []string{
			"bmc update",
			"bmc update --version <版本>",
			"bmc update --file <发布包路径>",
		},
		Options: []render.Option{
			{Name: "--version <版本>", Description: "从官方发布页下载并安装指定版本。", Default: "仅在有较新版本时更新"},
			{Name: "--file <发布包路径>", Description: "从本地框架发布 ZIP 更新。"},
		},
		Examples: []render.Example{
			{Command: "bmc update", Description: "读取官方发行版索引并应用可用更新。"},
		},
		Notes: []string{"联网更新读取官方发行版索引，按索引中的地址下载并校验发布包。每次请求最多等待 5 分钟，遇到临时网络错误最多重试两次。更新进度写入标准错误。更新完成后打开新的 Bash 终端。扩展包使用 bmc plugin upgrade 更新。"},
	},
	"use": {
		Summary: "在当前 Bash 中选择 BMC 目标并启动设备会话。",
		Usage:   []string{useUsage},
		Options: []render.Option{
			{Name: "<IP>", Description: "目标 BMC 的 IP 地址。"},
			{Name: "--resolver <解析器>", Description: "指定已安装解析器的包名或导出名，也可指定 generic。包名不带版本。", Default: "使用设置中的默认解析器，未配置时使用 generic"},
			{Name: "-- <解析器参数>...", Description: "将分隔符后的参数原样传给选定的解析器。"},
		},
		Examples: []render.Example{
			{Command: "bmc use 192.0.2.10 --resolver generic", Description: "选择示例目标 IP，跳过探测和登录验证。"},
		},
		Notes: []string{"查看解析器专用参数时，运行 bmc help use <解析器>。"},
	},
	"version": {
		Summary: "显示当前加载的 BMCShell 框架版本。",
		Usage:   []string{"bmc version", "bmc --version"},
		Examples: []render.Example{
			{Command: "bmc version", Description: "核对当前框架版本。"},
		},
	},
}

var genericResolverHelp = render.Topic{
	Summary: "仅选择目标 IP，跳过设备探测与登录验证。",
	Usage:   []string{"bmc use <IP> --resolver generic"},
	Examples: []render.Example{
		{Command: "bmc use 192.0.2.10 --resolver generic", Description: "选择示例目标 IP。"},
	},
}

package cli

import (
	"fmt"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/render"
	"bmcshell/internal/session"
)

var builtinHelp = map[string]render.Topic{
	"book":            {Summary: "列出和查询已安装的 SQLite 工具书。", Usage: []string{"bmc book list", "bmc book <ID-or-name> [-l | -s <keyword> | --id <id> | --name <name> | --key <values...>]", "bmc book <ID-or-name> --help"}},
	"doctor":          {Summary: "查看环境、插件和外部程序。", Usage: []string{"bmc doctor"}},
	"exit":            {Summary: "退出当前 BMC 会话并恢复终端。", Usage: []string{"bmc exit"}},
	"help":            {Summary: "查看命令说明或详细帮助。", Usage: []string{"bmc help [command path] [--usage]"}},
	"ipmi-proxy":      {Summary: "设置当前 Bash 的 IPMI 专用 SOCKS5 代理。", Usage: []string{"bmc ipmi-proxy [on|off|127.0.0.1:端口]"}, Notes: []string{"全局代理开启时优先使用全局地址。bmc proxy off 同时清除本终端的专用地址。"}},
	"library":         {Summary: "列出、查询和同步已安装的结构化库。", Usage: []string{"bmc library list", "bmc library query <ID-or-name> <key...>", "bmc library source <ID-or-name> <URL|--clear>", "bmc library refresh <ID-or-name> [--dry-run | --replace-all] [--source <name>]", "bmc library rollback <ID-or-name>"}},
	"library refresh": {Summary: "同步共享快照，结构变化时从软件源选择兼容版本。", Usage: []string{"bmc library refresh <ID-or-name> [--dry-run] [--source <name>]", "bmc library refresh <ID-or-name> --replace-all", "bmc library refresh <ID-or-name> [--dry-run] --upgrade-with <file> [--upgrade-with <file> ...]"}},
	"plugin":          {Summary: "管理软件源和扩展包。", Usage: []string{"bmc plugin list", "bmc plugin source list", "bmc plugin update [source]", "bmc plugin install <ID-or-name> ... [--source <name>]", "bmc plugin install --file <directory-or-zip>", "bmc plugin upgrade [<ID-or-name> ...] [--source <name>]", "bmc plugin show <ID-or-name> [--changelog]", "bmc plugin remove <ID-or-name>"}},
	"plugin install":  {Summary: "从索引安装扩展，或安装本地扩展目录及 ZIP。", Usage: []string{"bmc plugin install <ID-or-name> ... [--source <name>] [--dry-run]", "bmc plugin install --file <directory-or-zip>"}},
	"plugin source":   {Summary: "配置官方和自定义软件源。", Usage: []string{"bmc plugin source list", "bmc plugin source set-official <index-url>", "bmc plugin source add <name> <index-url>", "bmc plugin source remove <name>"}},
	"plugin update":   {Summary: "更新指定软件源或全部软件源的索引。", Usage: []string{"bmc plugin update [source]"}},
	"plugin upgrade":  {Summary: "从软件源预览和升级已安装扩展。", Usage: []string{"bmc plugin upgrade [<ID-or-name> ...] [--source <name>] [--dry-run | --yes]"}},
	"plugin show":     {Summary: "显示已安装包的元信息或变更记录。", Usage: []string{"bmc plugin show <ID-or-name> [--changelog]"}},
	"proxy":           {Summary: "查看或设置 BMCShell 的全局 SOCKS5 代理。", Usage: []string{"bmc proxy", "bmc proxy on", "bmc proxy 127.0.0.1:1081", "bmc proxy off"}, Notes: []string{"on 使用 127.0.0.1:1080。配置存入 settings.json 的 proxy.address。", "Redfish 走 SOCKS5 HTTPS，SSH 使用 CONNECT，IPMI 与 SNMP 使用 UDP ASSOCIATE。代理失败会返回错误。"}},
	"setting":         {Summary: "查看或修改框架设置。", Usage: []string{"bmc setting", "bmc setting --get <key>", "bmc setting --set <key> <value>", "bmc setting --reset <key>", "bmc setting --import-env <defaults.json>", "bmc setting --check"}},
	"status":          {Summary: "显示当前 BMC 会话字段。", Usage: []string{"bmc status"}},
	"tool":            {Summary: "列出工具或显示详细帮助。", Usage: []string{"bmc tool list", "bmc tool <name>"}},
	"use":             {Summary: "在当前 Bash 中选择一个 BMC 目标。", Usage: []string{"bmc use <IP> [--resolver <name>] [-- <resolver args...>]"}},
}

func (a *app) help(args []string) error {
	detailed := len(args) != 0 && args[len(args)-1] == "--usage"
	if detailed {
		args = args[:len(args)-1]
	}
	if len(args) == 0 {
		fmt.Fprintf(a.out, "BMCShell %s\n\n", a.version)
		for _, key := range sortedKeys(builtinHelp) {
			if !strings.Contains(key, " ") {
				fmt.Fprintf(a.out, "  %-9s %s\n", key, builtinHelp[key].Summary)
			}
		}
		fmt.Fprintln(a.out, "\nbmc help <command> --usage")
		return nil
	}
	key := strings.Join(args, " ")
	if topic, ok := builtinHelp[key]; ok {
		render.Help(a.out, topic, detailed)
		return nil
	}
	if len(args) != 2 || a.store == nil {
		return fmt.Errorf("unknown help topic %q", key)
	}
	var p packages.Package
	var topicKey string
	var err error
	switch args[0] {
	case "plugin", "book", "library":
		p, err = a.store.Find(args[1])
		topicKey = "package"
		if err == nil && args[0] != "plugin" && p.Type != args[0] {
			return fmt.Errorf("%q is a %s package", args[1], p.Type)
		}
	case "tool":
		var tool toolRef
		tool, err = a.findTool(args[1])
		p, topicKey = tool.Package, "tool."+args[1]
	case "use":
		if args[1] == "generic" {
			render.Help(a.out, render.Topic{Summary: "仅设置目标 IP。", Usage: []string{"bmc use <IP> --resolver generic"}}, detailed)
			return nil
		}
		list, listErr := a.store.List()
		if listErr != nil {
			return listErr
		}
		var resolver session.Resolver
		resolver, err = session.SelectResolver(list, args[1], nil)
		p, topicKey = resolver.Package, "resolver."+args[1]
	default:
		return fmt.Errorf("unknown help topic %q", key)
	}
	if err != nil {
		return err
	}
	var help struct {
		Topics  map[string]render.Topic
		Aliases map[string]string
	}
	if err := readPackageJSON(p, p.Help, &help); err != nil {
		return err
	}
	topic, ok := help.Topics[topicKey]
	if !ok {
		topic, ok = help.Topics[help.Aliases[topicKey]]
	}
	if !ok {
		return fmt.Errorf("package %s has no help for %q", p.Name, key)
	}
	render.Help(a.out, topic, detailed)
	return nil
}

func (a *app) changelog(p packages.Package) error {
	var history struct {
		Releases []struct {
			Version, Date string
			Changes       []string
		}
	}
	if err := readPackageJSON(p, p.Changelog, &history); err != nil {
		return err
	}
	fmt.Fprintln(a.out, p.Name)
	for _, release := range history.Releases {
		fmt.Fprintf(a.out, "\n%s  %s\n", release.Version, release.Date)
		for _, change := range release.Changes {
			fmt.Fprintln(a.out, "  - "+change)
		}
	}
	return nil
}

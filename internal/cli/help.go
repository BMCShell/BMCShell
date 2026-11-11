package cli

import (
	"fmt"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/render"
	"bmcshell/internal/session"
)

var builtinHelp = map[string]render.Topic{
	"book":    {Summary: "列出和查询只读工具书。", Usage: []string{"bmc book [-l|--list]", "bmc book <书名> [-h|--help|-v|--version]", "bmc book <书名> -s <关键词>", "bmc book <书名> <ID>"}},
	"doctor":  {Summary: "检查框架及插件依赖。", Usage: []string{"bmc doctor [-d|--detial] [-f|--filter <ok|missing>] [-a|--auto]"}},
	"exit":    {Summary: "退出当前设备会话并恢复终端。", Usage: []string{"bmc exit"}},
	"help":    {Summary: "查看命令或插件帮助。", Usage: []string{"bmc help [命令]", "bmc <命令> -h|--help"}},
	"library": {Summary: "列出和管理可操作的业务库。", Usage: []string{"bmc library [-l|--list]", "bmc library <库名>"}},
	"plugin":  {Summary: "搜索、安装、升级和卸载扩展包。", Usage: []string{"bmc plugin list [type]", "bmc plugin update", "bmc plugin search <关键词>", "bmc plugin install <name[@version]>", "bmc plugin install --file <路径>", "bmc plugin upgrade [name]", "bmc plugin remove <name>"}},
	"proxy":   {Summary: "设置当前终端和 BMCShell 的 SOCKS5 代理。", Usage: []string{"bmc proxy", "bmc proxy on|off", "bmc proxy -m 127.0.0.1:1081"}},
	"setting": {Summary: "查看和修改框架及插件设置。", Usage: []string{"bmc setting", "bmc setting --get <key>", "bmc setting --set <key> <value>", "bmc setting --set plugin.source.<name> <private-IP HTTPS URL> [--insecure]", "bmc setting --reset [key]", "bmc setting --check"}},
	"status":  {Summary: "查看当前目标 IP 和已登记的环境变量。", Usage: []string{"bmc status [--env]"}},
	"tool":    {Summary: "查看扩展命令、可用状态和来源。", Usage: []string{"bmc tool [-l|--list]", "bmc tool -a|--all", "bmc tool <工具名> [-h|--help]"}},
	"update":  {Summary: "检查并更新框架。", Usage: []string{"bmc update", "bmc update --version <version>", "bmc update --file <发布包>"}},
	"use":     {Summary: "在当前 Bash 中选中 BMC 目标 IP。", Usage: []string{"bmc use <IP> [--resolver <name>] [-- <resolver args...>]"}},
	"version": {Summary: "查看框架版本。", Usage: []string{"bmc version"}},
}

func (a *app) help(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(a.out, "BMCShell %s\n\n", a.version)
		for _, key := range sortedKeys(builtinHelp) {
			fmt.Fprintf(a.out, "  %-9s %s\n", key, builtinHelp[key].Summary)
		}
		fmt.Fprintln(a.out, "\nbmc help <command>")
		return nil
	}
	key := strings.Join(args, " ")
	if topic, ok := builtinHelp[key]; ok {
		render.Help(a.out, topic, true)
		return nil
	}
	if len(args) != 2 || a.store == nil {
		return fmt.Errorf("unknown help topic %q", key)
	}
	var p packages.Package
	var topicKey string
	var err error
	switch args[0] {
	case "book", "library":
		p, err = a.findType(args[1], args[0])
		topicKey = "package"
	case "tool":
		if resolver, ok := strings.CutPrefix(args[1], "use@"); ok {
			return a.help([]string{"use", resolver})
		}
		var tool toolRef
		tool, err = a.findTool(args[1])
		p, topicKey = tool.Package, "tool."+args[1]
	case "use":
		if args[1] == "generic" {
			render.Help(a.out, render.Topic{Summary: "仅选中目标 IP。", Usage: []string{"bmc use <IP> --resolver generic"}}, true)
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
	found, err := a.packageHelp(p, topicKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("package %s has no help for %q", p.Name, key)
	}
	return nil
}

func (a *app) packageHelp(p packages.Package, key string) (bool, error) {
	if p.Help == "" {
		return false, nil
	}
	var help struct {
		Topics  map[string]render.Topic
		Aliases map[string]string
	}
	if err := readPackageJSON(p, p.Help, &help); err != nil {
		return false, err
	}
	topic, ok := help.Topics[key]
	if !ok {
		topic, ok = help.Topics[help.Aliases[key]]
	}
	if ok {
		render.Help(a.out, topic, true)
	}
	return ok, nil
}

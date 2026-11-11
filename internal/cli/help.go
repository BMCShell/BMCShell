package cli

import (
	"fmt"
	"strings"

	"bmcshell/internal/packages"
	"bmcshell/internal/render"
	"bmcshell/internal/session"
)

func (a *app) help(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(a.out, "BMCShell %s\n\n命令\n", a.version)
		for _, key := range sortedKeys(builtinHelp) {
			if strings.ContainsRune(key, ' ') {
				continue
			}
			fmt.Fprintf(a.out, "  %-9s %s\n", key, builtinHelp[key].Summary)
		}
		fmt.Fprintln(a.out, "\n运行 bmc help <命令> 查看详细帮助。")
		return nil
	}
	key := strings.Join(args, " ")
	if topic, ok := builtinHelp[key]; ok {
		render.Help(a.out, topic, true)
		return nil
	}
	if len(args) != 2 || a.store == nil {
		return fmt.Errorf("未找到帮助主题 %q。运行 bmc help 查看可用命令", key)
	}
	var p packages.Package
	var topicKey string
	var err error
	switch args[0] {
	case "book":
		return a.book([]string{args[1], "--help"})
	case "library":
		p, err = a.findType(args[1], "library")
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
			render.Help(a.out, genericResolverHelp, true)
			return nil
		}
		list, listErr := a.store.List()
		if listErr != nil {
			return listErr
		}
		var resolver session.Resolver
		resolver, err = session.SelectResolver(list, args[1], nil)
		p, topicKey = resolver.Package, "resolver."+resolver.Name
	default:
		return fmt.Errorf("未找到帮助主题 %q。运行 bmc help 查看可用命令", key)
	}
	if err != nil {
		return err
	}
	found, err := a.packageHelp(p, topicKey)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("扩展包 %s 未提供 %q 的帮助", p.Name, key)
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

package cli

import (
	"fmt"
	"slices"
	"strings"

	"bmcshell/internal/packages"
)

func (e *settingsEditor) themeOptions() []settingOption {
	options := []settingOption{{value: "none", label: "关闭主题", hint: "使用 Bash 原有的提示符。"}}
	for _, p := range e.installed {
		if p.Type == "theme" {
			options = append(options, settingOption{value: p.ID, label: p.Title(), hint: p.Details()})
		}
	}
	return options
}

func (e *settingsEditor) resolverOptions() []settingOption {
	options := []settingOption{{value: "generic", label: "通用解析器", hint: "记录目标地址，无需安装额外解析器。"}}
	for _, p := range e.installed {
		for _, name := range sortedKeys(p.Provides.Resolvers) {
			label := p.Title()
			if len(p.Provides.Resolvers) > 1 {
				label += " / " + name
			}
			options = append(options, settingOption{value: p.ID + "/" + name, label: label, hint: p.Details()})
		}
	}
	return options
}

func pluginMenuItem(p packages.Package, field packages.PluginSetting) settingItem {
	item := settingItem{
		key: packageSettingKey(p, field), label: field.Label, plugin: &field,
		hint: field.Hint, defaultValue: field.DefaultValue(),
	}
	switch field.Type {
	case "enum":
		for _, option := range field.Options {
			item.options = append(item.options, settingOption{value: option.Value, label: option.Label, hint: option.Hint})
		}
		if item.hint == "" {
			item.hint = "使用方向键选择一个可用值。"
		}
	case "boolean":
		item.options = []settingOption{{value: "true", label: "开启"}, {value: "false", label: "关闭"}}
		if item.hint == "" {
			item.hint = "选择开启或关闭。"
		}
	case "integer":
		item.number = true
		item.signed = field.Min == nil || *field.Min < 0
		bounds := "输入整数"
		if field.Min != nil {
			bounds += fmt.Sprintf("，最小值 %d", *field.Min)
		}
		if field.Max != nil {
			bounds += fmt.Sprintf("，最大值 %d", *field.Max)
		}
		if item.hint != "" {
			item.hint += " "
		}
		item.hint += bounds + "。"
	default:
		if item.hint == "" {
			item.hint = "输入单行文本，可留空。"
		}
	}
	return item
}

type pluginSettingsNode struct {
	label string
	hint  string
	nodes []*pluginSettingsNode
	items []settingItem
}

func (n *pluginSettingsNode) child(label, hint string) *pluginSettingsNode {
	for _, node := range n.nodes {
		if node.label == label {
			return node
		}
	}
	node := &pluginSettingsNode{label: label, hint: hint}
	n.nodes = append(n.nodes, node)
	return node
}

var settingPathLabels = map[string]string{
	"appearance": "外观", "theme": "主题", "connection": "连接设置", "resolvers": "解析器",
	"network": "网络连接", "snmp": "SNMP", "tools": "工具", "other": "其他",
}

func (e *settingsEditor) pluginMenu() error {
	root := &pluginSettingsNode{}
	for _, p := range e.installed {
		for _, field := range p.Settings {
			path := field.Path
			if path == "" {
				switch {
				case p.Type == "theme":
					path = "appearance.theme"
				case len(p.Provides.Resolvers) != 0:
					path = "connection.resolvers"
				default:
					path = "other"
				}
			}
			node := root
			for _, segment := range strings.Split(path, ".") {
				label := settingPathLabels[segment]
				if label == "" {
					label = segment
				}
				node = node.child(label, "浏览该分组下的插件参数。")
			}
			// A package UUID distinguishes packages that use the same display name.
			node = node.child(p.Title()+" ("+p.Name+")", p.Details())
			node.items = append(node.items, pluginMenuItem(p, field))
		}
	}
	return e.pluginNodeMenu("设置中心 / 插件设置", root)
}

func (e *settingsEditor) pluginNodeMenu(title string, node *pluginSettingsNode) error {
	cursor := 0
	slices.SortStableFunc(node.nodes, func(a, b *pluginSettingsNode) int { return strings.Compare(a.label, b.label) })
	for {
		var options []settingOption
		for _, child := range node.nodes {
			options = append(options, settingOption{label: child.label, hint: child.hint})
		}
		for _, item := range node.items {
			label := item.label + "    " + item.display(e.value(e.draft.Config, item))
			if e.changed(item) {
				label += " · 未保存"
			}
			options = append(options, settingOption{label: label, hint: item.hint})
		}
		if len(options) == 0 {
			options = append(options, settingOption{label: "返回", hint: "当前没有已安装插件声明参数。"})
			_, err := e.ui.choose(title, nil, options, 0, "返回")
			return err
		}
		choice, err := e.ui.choose(title, nil, options, cursor, "返回")
		if err != nil || choice < 0 {
			return err
		}
		cursor = choice
		if choice < len(node.nodes) {
			child := node.nodes[choice]
			err = e.pluginNodeMenu(title+" / "+child.label, child)
		} else {
			err = e.itemMenu(title, node.items[choice-len(node.nodes)])
		}
		if err != nil {
			return err
		}
	}
}

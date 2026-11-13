package cli

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
	"bmcshell/internal/settings"
)

type settingOption struct {
	value, label, hint string
}

type settingItem struct {
	key, label, hint, defaultValue string
	options                        []settingOption
	number, signed                 bool
	plugin                         *packages.PluginSetting
}

type settingGroup struct {
	label, hint string
	items       []settingItem
}

type settingsEditor struct {
	app       *app
	draft     *settings.Draft
	ui        *settingUI
	installed []packages.Package
	catalog   *catalog.Store
}

var tlsSettingOptions = []settingOption{
	{value: "verify", label: "校验证书", hint: "验证 HTTPS 服务器的证书。"},
	{value: "insecure", label: "跳过证书校验", hint: "建立 HTTPS 连接时跳过证书校验。"},
}

func (a *app) settingsMenu() (err error) {
	draft, err := settings.OpenDraft(a.home)
	if err != nil {
		return err
	}
	defer func() {
		if draft != nil {
			draft.Close()
		}
	}()
	ui := newSettingUI(a.in, a.out)
	defer func() { err = errors.Join(err, ui.close()) }()
	e := settingsEditor{app: a, draft: draft, ui: ui, catalog: catalog.Open(a.home)}
	if draft.Recovered {
		options := []settingOption{
			{label: "继续编辑草稿", hint: "恢复上次意外退出前确认的修改。"},
			{label: "丢弃草稿", hint: "从已保存的配置重新开始。"},
		}
		choice, err := ui.choose("设置中心 / 恢复草稿", nil, options, 0, "退出")
		if err != nil || choice < 0 {
			return settingExitError(err)
		}
		if choice == 1 {
			if err := draft.Discard(); err != nil {
				return err
			}
			draft.Close()
			draft, err = settings.OpenDraft(a.home)
			if err != nil {
				return err
			}
			e.draft = draft
		}
	}
	return settingExitError(e.run())
}

func settingExitError(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func (e *settingsEditor) run() error {
	cursor := 0
	for {
		installed, err := e.app.store.List()
		if err != nil {
			return err
		}
		slices.SortFunc(installed, func(a, b packages.Package) int { return strings.Compare(a.Name, b.Name) })
		e.installed = installed
		groups, err := e.groups()
		if err != nil {
			return err
		}
		var options []settingOption
		for _, group := range groups {
			options = append(options, settingOption{label: group.label, hint: group.hint})
		}
		options = append(options,
			settingOption{label: "软件源", hint: "添加软件源，修改地址或 TLS 校验方式。"},
			settingOption{label: "插件设置", hint: "按清单路径浏览和配置已安装插件。"},
		)
		choice, err := e.ui.choose("设置中心", nil, options, cursor, "退出")
		if err != nil {
			return err
		}
		switch {
		case choice < 0:
			exit, err := e.confirmExit()
			if err != nil || exit {
				return err
			}
		case choice < len(groups):
			err = e.groupMenu(groups[choice])
		case choice == len(groups):
			err = e.sourceMenu()
		default:
			err = e.pluginMenu()
		}
		if err != nil {
			return err
		}
		if choice >= 0 {
			cursor = choice
		}
	}
}

func (e *settingsEditor) groups() ([]settingGroup, error) {
	registry, err := e.app.store.Environment(e.installed)
	if err != nil {
		return nil, err
	}
	groups := []settingGroup{
		{"外观", "选择终端主题，调整当前主题的显示参数。", []settingItem{
			{key: "theme.active", label: "终端主题", hint: "选择已安装主题。保存后刷新当前终端的提示符。", defaultValue: "none"},
			{key: "theme.override", label: "覆盖 Bash 提示符", hint: "让活动脚本主题在其他提示符命令之后绘制。", defaultValue: "false",
				options: []settingOption{{value: "true", label: "开启"}, {value: "false", label: "关闭"}}},
		}},
		{"目标与认证", "管理默认解析器和连接默认值。", []settingItem{
			{key: "use.default_resolver", label: "默认解析器", hint: "下次运行 bmc use 时使用。", defaultValue: "generic"},
			{key: "BMCSHELL_TARGET_IP", label: "目标 IP 地址", hint: "输入 IPv4 或 IPv6 地址。"},
			{key: "BMCSHELL_TARGET_MODEL", label: "目标设备型号", hint: "为支持设备型号的工具设置默认值。"},
			{key: "BMCSHELL_TARGET_RESOLVER", label: "目标解析器", hint: "为读取解析器名称的工具设置默认值。"},
			{key: "BMCSHELL_AUTH_USERNAME", label: "认证用户名", hint: "设置工具使用的默认认证用户名。"},
		}},
		{"网络连接", "配置 HTTPS 证书校验和 IPMI 代理。", []settingItem{
			{key: "BMCSHELL_TLS_MODE", label: "TLS 证书校验", hint: "设置设备 HTTPS 连接的证书校验方式。", options: tlsSettingOptions},
			{key: "BMCSHELL_IPMI_PROXY", label: "IPMI SOCKS5 代理", hint: "输入 127.0.0.1:端口，留空表示无默认代理。"},
		}},
		{"SNMP", "设置默认 SNMP 版本及企业 OID。", []settingItem{
			{key: "BMCSHELL_SNMP_VERSION", label: "SNMP 版本", hint: "选择 SNMP 工具默认使用的协议版本。", options: []settingOption{
				{value: "1", label: "1", hint: "使用 SNMPv1。"}, {value: "2c", label: "2c", hint: "使用 SNMPv2c。"}, {value: "3", label: "3", hint: "使用 SNMPv3。"},
			}},
			{key: "BMCSHELL_SNMP_ENTERPRISES", label: "SNMP 企业编号", hint: "输入企业编号或点分 OID 前缀，允许留空。"},
		}},
	}
	known := make(map[string]bool)
	for i := range groups {
		for j := range groups[i].items {
			item := &groups[i].items[j]
			known[item.key] = true
			if field, ok := registry[item.key]; ok && field.FrameworkDefault != nil {
				item.defaultValue = *field.FrameworkDefault
			}
		}
	}
	other := settingGroup{label: "其他设置", hint: "配置已登记的非敏感环境默认值。"}
	for _, name := range sortedKeys(registry) {
		field := registry[name]
		if known[name] || field.Sensitive {
			continue
		}
		item := settingItem{key: name, label: name, hint: "设置该环境变量的默认值。"}
		if field.PluginDefault != nil {
			item.defaultValue = *field.PluginDefault
		}
		if field.FrameworkDefault != nil {
			item.defaultValue = *field.FrameworkDefault
		}
		other.items = append(other.items, item)
	}
	if len(other.items) != 0 {
		groups = append(groups, other)
	}
	return groups, nil
}

func (e *settingsEditor) groupMenu(group settingGroup) error {
	cursor := 0
	for {
		items := slices.Clone(group.items)
		if group.label == "外观" {
			if p, err := e.app.activeTheme(e.draft.Config); err == nil {
				for _, field := range p.Settings {
					items = append(items, pluginMenuItem(p, field))
				}
			}
		}
		if group.label == "目标与认证" && e.draft.Config.DefaultResolver != nil {
			id := e.draft.Config.DefaultResolver.ExtensionID
			for _, p := range e.installed {
				if p.ID == id {
					for _, field := range p.Settings {
						items = append(items, pluginMenuItem(p, field))
					}
				}
			}
		}
		for i := range items {
			switch items[i].key {
			case "theme.active":
				items[i].options = e.themeOptions()
			case "use.default_resolver":
				items[i].options = e.resolverOptions()
			}
		}
		title := "设置中心 / " + group.label
		options := make([]settingOption, 0, len(items))
		for _, item := range items {
			label := item.label + "    " + item.display(e.value(e.draft.Config, item))
			if e.changed(item) {
				label += " · 未保存"
			}
			options = append(options, settingOption{label: label, hint: item.hint})
		}
		choice, err := e.ui.choose(title, nil, options, cursor, "返回")
		if err != nil || choice < 0 {
			return err
		}
		cursor = choice
		if err := e.itemMenu(title, items[choice]); err != nil {
			return err
		}
	}
}

func (e *settingsEditor) value(cfg settings.Config, item settingItem) string {
	if item.key == "theme.active" && cfg.Theme != "" {
		return cfg.Theme
	}
	if item.key == "use.default_resolver" && cfg.DefaultResolver != nil {
		return cfg.DefaultResolver.ExtensionID + "/" + cfg.DefaultResolver.Export
	}
	value, err := e.app.settingValue(cfg, item.key)
	if err != nil {
		return "不可用"
	}
	return value
}

func (e *settingsEditor) changed(item settingItem) bool {
	base, draft := e.draft.Base, e.draft.Config
	switch item.key {
	case "theme.active":
		return base.Theme != draft.Theme
	case "theme.override":
		return base.ThemeOverride != draft.ThemeOverride
	case "use.default_resolver":
		return !reflect.DeepEqual(base.DefaultResolver, draft.DefaultResolver)
	}
	if item.plugin != nil {
		id, _, _ := strings.Cut(strings.TrimPrefix(item.key, pluginSettingPrefix), ".")
		old, oldSet := pluginSettingOverride(base, id, item.plugin.Key)
		newValue, newSet := pluginSettingOverride(draft, id, item.plugin.Key)
		return oldSet != newSet || old != newValue
	}
	old, oldSet := base.EnvDefaults[item.key]
	newValue, newSet := draft.EnvDefaults[item.key]
	return oldSet != newSet || old != newValue
}

func (item settingItem) display(value string) string {
	for _, option := range item.options {
		if option.value == value {
			return option.label
		}
	}
	if value == "" {
		return "未设置"
	}
	if item.key == "theme.active" || item.key == "use.default_resolver" {
		return "不可用"
	}
	return value
}

func (e *settingsEditor) itemMenu(parent string, item settingItem) error {
	title := parent + " / " + item.label
	current := e.value(e.draft.Config, item)
	details := []string{
		"默认值    " + item.display(item.defaultValue),
		"已保存    " + item.display(e.value(e.draft.Base, item)),
	}
	if e.changed(item) {
		details = append(details, "本次编辑  "+item.display(current)+" · 未保存")
	}
	if item.plugin != nil && item.plugin.Env != "" {
		details = append(details, "环境变量  "+item.plugin.Env)
	}
	if len(item.options) != 0 {
		options := slices.Clone(item.options)
		index := 0
		for i := range options {
			if options[i].hint == "" {
				options[i].hint = item.hint
			}
			if options[i].value == current {
				index = i
				options[i].label += "（当前值）"
			}
		}
		options = append(options, settingOption{label: "恢复默认值", hint: "清除本项的自定义值。"})
		choice, err := e.ui.choose(title, details, options, index, "返回")
		if err != nil || choice < 0 {
			return err
		}
		var value *string
		if choice < len(item.options) {
			value = &item.options[choice].value
		}
		return e.edit(item.key, value)
	}
	options := []settingOption{
		{label: "修改", hint: item.hint},
		{label: "恢复默认值", hint: "清除本项的自定义值。"},
	}
	choice, err := e.ui.choose(title, details, options, 0, "返回")
	if err != nil || choice < 0 {
		return err
	}
	if choice == 1 {
		return e.edit(item.key, nil)
	}
	if current == "不可用" {
		current = ""
	}
	return e.ui.edit(title, item.hint, details, current, item.number, item.signed, func(value string) error {
		return e.edit(item.key, &value)
	})
}

func (e *settingsEditor) edit(key string, value *string) error {
	next := settings.Clone(e.draft.Config)
	if err := e.app.editSetting(&next, key, value); err != nil {
		return err
	}
	previous := e.draft.Config
	e.draft.Config = next
	if err := e.draft.Checkpoint(); err != nil {
		e.draft.Config = previous
		return err
	}
	return nil
}

func (e *settingsEditor) confirmExit() (bool, error) {
	if !e.draft.Dirty() {
		return true, nil
	}
	options := []settingOption{
		{label: "保存并退出", hint: "写入正式配置，重载当前终端环境。"},
		{label: "不保存退出", hint: "丢弃本次修改。"},
		{label: "继续编辑", hint: "返回设置中心。"},
	}
	var details []string
	for {
		summary := []string{"有未保存的修改。"}
		choice, err := e.ui.choose("退出设置中心", append(summary, details...), options, 0, "继续编辑")
		if err != nil {
			return false, err
		}
		switch choice {
		case 0:
			if err := e.commit(); err != nil {
				details = []string{err.Error()}
				continue
			}
			if err := e.draft.Discard(); err != nil {
				fmt.Fprintln(e.app.errOut, "设置已保存，清理草稿失败。", err)
			}
			if err := writeSettingCommitResult(); err != nil {
				return true, fmt.Errorf("设置已保存，请运行 bmc setting --reload：%w", err)
			}
			return true, nil
		case 1:
			return true, e.draft.Discard()
		default:
			return false, nil
		}
	}
}

func (e *settingsEditor) commit() error {
	base, draft := e.draft.Base, e.draft.Config
	var removedSources []string
	err := settings.Update(e.app.home, func(current *settings.Config) error {
		if draft.Sources != nil && !reflect.DeepEqual(base.Sources, draft.Sources) {
			if current.Sources == nil {
				sources, err := e.catalog.ConfigSources(*current)
				if err != nil {
					return err
				}
				if sources == nil {
					sources = []catalog.Source{}
				}
				current.Sources = &sources
			}
		}
		merged, err := settings.MergeDraft(base, draft, *current)
		if err != nil {
			return err
		}
		if err := e.validateEdits(base, draft, merged); err != nil {
			return err
		}
		if draft.Sources != nil && base.Sources != nil {
			for _, source := range *base.Sources {
				if !slices.ContainsFunc(*draft.Sources, func(p settings.Source) bool { return p.Name == source.Name }) {
					removedSources = append(removedSources, source.Name)
				}
			}
		}
		*current = merged
		return nil
	})
	if err == nil && len(removedSources) != 0 {
		e.catalog.PruneRemovedSourceCaches(removedSources)
	}
	return err
}

func (e *settingsEditor) validateEdits(base, draft, merged settings.Config) error {
	if base.Theme != draft.Theme && merged.Theme != "" {
		if _, err := e.app.activeTheme(merged); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(base.DefaultResolver, draft.DefaultResolver) && merged.DefaultResolver != nil {
		ref := merged.DefaultResolver
		p, err := e.app.store.Find(ref.ExtensionID)
		if err != nil {
			return err
		}
		if _, ok := p.Provides.Resolvers[ref.Export]; !ok {
			return fmt.Errorf("解析器 %q 已不可用", ref.Export)
		}
	}
	for key, value := range draft.EnvDefaults {
		if old, ok := base.EnvDefaults[key]; ok && old == value {
			continue
		}
		field, err := e.app.settingField(key)
		if err != nil {
			return err
		}
		if err := field.Validate(value); err != nil {
			return err
		}
	}
	for id, values := range draft.PluginSettings {
		for key, value := range values {
			if old, ok := base.PluginSettings[id][key]; ok && old == value {
				continue
			}
			_, field, err := e.app.pluginSetting(merged, pluginSettingPrefix+id+"."+key)
			if err != nil {
				return err
			}
			if err := field.Validate(value); err != nil {
				return err
			}
		}
	}
	_, err := e.catalog.ConfigSources(merged)
	return err
}

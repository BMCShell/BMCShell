package cli

import (
	"errors"
	"slices"
	"strings"

	"bmcshell/internal/catalog"
)

func sourceSettingLabel(name string) string {
	if name == "official" {
		return "官方软件源"
	}
	return name + " 软件源"
}

func (e *settingsEditor) sourceMenu() error {
	title := "设置中心 / 软件源"
	cursor := 0
	for {
		sources, err := e.catalog.ConfigSources(e.draft.Config)
		if err != nil {
			return err
		}
		options := make([]settingOption, 0, len(sources)+1)
		for _, source := range sources {
			options = append(options, settingOption{label: sourceSettingLabel(source.Name), hint: redactSource(source.URL)})
		}
		options = append(options, settingOption{label: "添加软件源", hint: "指定名称、地址和 TLS 校验方式。"})
		choice, err := e.ui.choose(title, nil, options, cursor, "返回")
		if err != nil || choice < 0 {
			return err
		}
		cursor = choice
		if choice == len(sources) {
			err = e.addSource(title, sources)
		} else {
			err = e.editSource(title, sources, choice)
		}
		if err != nil {
			return err
		}
	}
}

func (e *settingsEditor) addSource(title string, sources []catalog.Source) error {
	title += " / 添加软件源"
	var source catalog.Source
	if err := e.ui.edit(title, "用小写字母开头，可含数字和连字符。", nil, "", false, false, func(value string) error {
		name := strings.TrimSpace(value)
		for _, existing := range sources {
			if existing.Name == name {
				return errors.New("此软件源名称已存在。")
			}
		}
		candidate := catalog.Source{Name: name, Official: name == "official", URL: "https://example.test/index.json"}
		if err := catalog.ValidateSource(candidate); err != nil {
			return err
		}
		source.Name, source.Official = candidate.Name, candidate.Official
		return nil
	}); err != nil || source.Name == "" {
		return err
	}
	if err := e.ui.edit(title, "输入软件源 HTTPS 地址。", nil, "", false, false, func(value string) error {
		candidate := source
		candidate.URL = strings.TrimSpace(value)
		if err := catalog.ValidateSource(candidate); err != nil {
			return err
		}
		source = candidate
		return nil
	}); err != nil || source.URL == "" {
		return err
	}
	for {
		choice, err := e.ui.choose(title, nil, tlsSettingOptions, 0, "返回")
		if err != nil || choice < 0 {
			return err
		}
		source.InsecureTLS = choice == 1
		if err := e.stageSources(append(slices.Clone(sources), source)); err != nil {
			if err := e.sourceError(title, err); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (e *settingsEditor) editSource(parent string, sources []catalog.Source, index int) error {
	source := sources[index]
	title := parent + " / " + sourceSettingLabel(source.Name)
	tls := 0
	if source.InsecureTLS {
		tls = 1
	}
	options := []settingOption{
		{label: "修改地址", hint: "输入新的软件源地址。"},
		{label: "修改 TLS 证书校验", hint: "单独调整此软件源的 HTTPS 校验。"},
		{label: "移除软件源", hint: "保存后此软件源将从配置中移除。"},
	}
	choice, err := e.ui.choose(title, []string{"地址    " + redactSource(source.URL), "TLS     " + tlsSettingOptions[tls].label}, options, 0, "返回")
	if err != nil {
		return err
	}
	switch choice {
	case 0:
		return e.ui.edit(title, options[0].hint, nil, redactSource(source.URL), false, false, func(value string) error {
			next := source
			next.URL = strings.TrimSpace(value)
			if next.URL == redactSource(source.URL) {
				next.URL = source.URL
			}
			sources[index] = next
			return e.stageSources(sources)
		})
	case 1:
		selected, err := e.ui.choose(title, nil, tlsSettingOptions, tls, "返回")
		if err != nil || selected < 0 {
			return err
		}
		sources[index].InsecureTLS = selected == 1
		if err := e.stageSources(sources); err != nil {
			return e.sourceError(title, err)
		}
	case 2:
		confirm := []settingOption{
			{label: "取消", hint: "保留此软件源。"},
			{label: "确认移除", hint: "将移除操作加入本次草稿。"},
		}
		selected, err := e.ui.choose(title, nil, confirm, 0, "返回")
		if err != nil {
			return err
		}
		if selected == 1 {
			return e.stageSources(slices.Delete(sources, index, index+1))
		}
	}
	return nil
}

func (e *settingsEditor) sourceError(title string, failure error) error {
	_, err := e.ui.choose(title, []string{failure.Error()}, []settingOption{{label: "返回", hint: "检查地址和 TLS 校验配置。"}}, 0, "返回")
	return err
}

func (e *settingsEditor) stageSources(sources []catalog.Source) error {
	if err := catalog.ValidateSources(sources); err != nil {
		return err
	}
	previousBase := e.draft.Base.Sources
	if previousBase == nil {
		base, err := e.catalog.ConfigSources(e.draft.Base)
		if err != nil {
			return err
		}
		if base == nil {
			base = []catalog.Source{}
		}
		e.draft.Base.Sources = &base
	}
	if sources == nil {
		sources = []catalog.Source{}
	}
	previous := e.draft.Config.Sources
	e.draft.Config.Sources = &sources
	if err := e.draft.Checkpoint(); err != nil {
		e.draft.Config.Sources = previous
		e.draft.Base.Sources = previousBase
		return err
	}
	return nil
}

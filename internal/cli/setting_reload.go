package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

const settingResultEnv = "BMCSHELL_BRIDGE_SETTING_RESULT"

func (a *app) settingEnvironmentBridge(args []string) error {
	if len(args) != 0 {
		return errors.New("bridge setting-environment 不接受参数")
	}
	cfg, err := settings.Load(a.home)
	if err != nil {
		return err
	}
	values, err := a.pluginSettingEnvironment(cfg)
	if err != nil {
		return err
	}
	// The legacy SNMP wrapper reads the exported default when no session or
	// command-line version is set. Without an override it keeps its own default.
	const snmpVersion = "BMCSHELL_SNMP_VERSION"
	if value, ok := cfg.EnvDefaults[snmpVersion]; ok {
		field, err := a.settingField(snmpVersion)
		if err != nil {
			return err
		}
		if err := field.Validate(value); err != nil {
			return err
		}
		values = append(values, [2]string{snmpVersion, value})
	}
	var response strings.Builder
	fmt.Fprintf(&response, "settings-env-v1\t%s\n", cfg.Proxy)
	for _, pair := range values {
		if !envregistry.ValidName(pair[0]) || !session.Printable(pair[1]) {
			return fmt.Errorf("设置环境变量 %q 无效", pair[0])
		}
		fmt.Fprintf(&response, "%s=%s\n", pair[0], pair[1])
	}
	_, err = io.WriteString(a.out, response.String())
	return err
}

func writeSettingCommitResult() error {
	path := os.Getenv(settingResultEnv)
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 0 {
		return errors.New("设置重载通知文件无效")
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		return errors.Join(err, f.Close())
	}
	if !os.SameFile(info, opened) || opened.Size() != 0 {
		return errors.Join(errors.New("设置重载通知文件已变更"), f.Close())
	}
	_, writeErr := f.WriteString("committed\n")
	return errors.Join(writeErr, f.Close())
}

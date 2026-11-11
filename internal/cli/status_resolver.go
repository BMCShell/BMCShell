package cli

import (
	"fmt"

	"bmcshell/internal/session"
	"bmcshell/internal/settings"
)

func (a *app) statusResolver(ctx session.Context) error {
	selector := ctx["target.resolver"]
	if selector == "" || selector == "generic" {
		return nil
	}
	list, err := a.store.List()
	if err != nil {
		return err
	}
	resolver, err := session.SelectResolver(list, selector, nil)
	if err != nil {
		// A session may outlive the installed resolver that created it.
		return nil
	}
	fields := resolver.Package.Provides.Resolvers[resolver.Name].StatusFields
	if len(fields) == 0 {
		return nil
	}
	registry, err := a.store.Environment(list)
	if err != nil {
		return err
	}
	for _, status := range fields {
		field, ok := registry.ByKey(status.Key)
		if !ok || field.Sensitive {
			continue
		}
		value := ctx[status.Key]
		if status.Key == "snmp.enterprises" {
			cfg, err := settings.Load(a.home)
			if err != nil {
				return err
			}
			value, _ = environmentValue(field, ctx, cfg.EnvDefaults, shellRuntimeValues[field.Name])
		}
		if value == "" {
			continue
		}
		if field.Validate(value) != nil {
			return fmt.Errorf("设备状态字段 %s 的值无效", field.Name)
		}
		fmt.Fprintf(a.out, "%s：%s\n", status.Label, value)
	}
	return nil
}

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"bmcshell/internal/envregistry"
	"bmcshell/internal/packages"
)

const (
	resolverLibrariesEnv = "BMCSHELL_RESOLVER_LIBRARIES"
	resolverBashEnv      = "BMCSHELL_RESOLVER_BASH"
	resolverCoreExeEnv   = "BMCSHELL_RESOLVER_CORE_EXE"
)

func (a *app) resolverEnvironment(p packages.Package, installed []packages.Package, registry envregistry.Registry) ([]string, error) {
	libraries := make(map[string]string, len(p.LibrarySchemas))
	for id, supported := range p.LibrarySchemas {
		index := slices.IndexFunc(installed, func(candidate packages.Package) bool { return candidate.ID == id })
		if index < 0 || installed[index].Library == nil || !supported.Supports(installed[index].Library.SchemaVersion) {
			return nil, fmt.Errorf("解析器 %s 需要业务库 %s 的结构版本 %d–%d", p.Name, id, supported.Min, supported.Max)
		}
		state, err := a.libraryFiles(installed[index])
		if err != nil {
			return nil, fmt.Errorf("无法读取业务库 %s：%w", id, err)
		}
		path, err := filepath.Abs(state.work)
		if err != nil {
			return nil, fmt.Errorf("无法读取业务库 %s：%w", id, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("无法读取业务库 %s：%w", id, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("无法读取业务库 %s：工作库必须是普通文件", id)
		}
		libraries[id] = path
	}
	encoded, err := json.Marshal(libraries)
	if err != nil {
		return nil, err
	}
	var bash string
	if path := os.Getenv("BMCSHELL_BRIDGE_BASH"); os.Getenv("BMCSHELL_BRIDGE_INTERACTIVE") == "1" && filepath.IsAbs(path) {
		bash = path
	}
	core, err := os.Executable()
	if err != nil || !filepath.IsAbs(core) {
		core = ""
	}
	env := slices.DeleteFunc(toolEnvironment(registry, true), func(pair string) bool {
		name, _, _ := strings.Cut(pair, "=")
		switch strings.ToUpper(name) {
		case resolverLibrariesEnv, resolverBashEnv, resolverCoreExeEnv:
			return true
		}
		return false
	})
	return append(env, resolverLibrariesEnv+"="+string(encoded), resolverBashEnv+"="+bash, resolverCoreExeEnv+"="+core), nil
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
	"bmcshell/internal/release"
)

const coreUpdateRequestTimeout = 5 * time.Minute

func (a *app) update(args []string) error {
	var requested, archivePath string
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "--version":
		requested = args[1]
		if err := packages.ValidateVersion(requested); err != nil {
			return err
		}
	case len(args) == 2 && args[0] == "--file" && args[1] != "":
		archivePath = args[1]
	default:
		return errors.New("框架更新参数无效。运行 bmc update --help 查看用法")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if archivePath == "" {
		if err := release.CheckInstallation(executable); err != nil {
			return err
		}
		address, err := a.proxyAddress()
		if err != nil {
			return err
		}
		client := &http.Client{Transport: proxy.HTTPTransport(address), Timeout: coreUpdateRequestTimeout}
		defer client.CloseIdleConnections()
		ctx := context.Background()
		fmt.Fprintf(a.errOut, "正在读取框架发行版索引（单次网络请求最长 %d 分钟）…\n", coreUpdateRequestTimeout/time.Minute)
		available, err := release.SelectIndexWithProgress(ctx, client, requested, a.errOut)
		if err != nil {
			return err
		}
		if requested == "" && catalog.CompareVersion(available.Version, a.version) <= 0 {
			fmt.Fprintf(a.out, "当前 BMCShell %s 已是最新版本。\n", a.version)
			return nil
		}
		fmt.Fprintf(a.errOut, "当前版本 %s，目标版本 %s。\n", a.version, available.Version)
		dir, err := os.MkdirTemp("", "bmcshell-core-update-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		archivePath, err = available.Download(ctx, client, dir, a.errOut)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.errOut, "正在读取本地框架发布包 %q…\n", filepath.Base(archivePath))
	}
	version, err := release.Install(executable, archivePath, a.errOut)
	if err != nil {
		return err
	}
	if _, portable := release.PortableRoot(executable); !portable {
		fmt.Fprintf(a.out, "BMCShell %s 已就绪。请在新的 Bash 终端中加载安装目录的 bin/bmcshell.sh。\n", version)
		return nil
	}
	fmt.Fprintf(a.out, "BMCShell %s 已就绪。请打开新的 Bash 终端加载。\n", version)
	return nil
}

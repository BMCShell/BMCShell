package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
	"bmcshell/internal/release"
)

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
		return errors.New("usage: bmc update [--version <version> | --file <release ZIP>]")
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
		client := &http.Client{Transport: proxy.HTTPTransport(address), Timeout: 90 * time.Second}
		defer client.CloseIdleConnections()
		ctx := context.Background()
		available, err := release.SelectGitHub(ctx, client, requested)
		if err != nil {
			return err
		}
		if requested == "" && catalog.CompareVersion(available.Version, a.version) <= 0 {
			fmt.Fprintf(a.out, "BMCShell %s is current.\n", a.version)
			return nil
		}
		dir, err := os.MkdirTemp("", "bmcshell-core-update-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		archivePath, err = available.Download(ctx, client, dir)
		if err != nil {
			return err
		}
	}
	version, err := release.Install(executable, archivePath)
	if err != nil {
		return err
	}
	if _, portable := release.PortableRoot(executable); !portable {
		fmt.Fprintf(a.out, "BMCShell %s is ready. Source bin/bmcshell.sh from the installation in a new Bash terminal.\n", version)
		return nil
	}
	fmt.Fprintf(a.out, "BMCShell %s is ready. Open a new Bash terminal to load it.\n", version)
	return nil
}

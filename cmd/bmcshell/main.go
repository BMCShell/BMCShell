package main

import (
	"os"

	"bmcshell/internal/cli"
)

var version = "3.0.0-dev.3"

func main() {
	os.Exit(cli.Run(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

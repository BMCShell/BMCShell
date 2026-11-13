package cli

import (
	"io"
	"os"
)

const (
	outputSuccess = "\x1b[32m"
	outputWarning = "\x1b[33m"
	outputError   = "\x1b[31m"
)

func outputColor(writer io.Writer, style, label string) string {
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
		return label
	}
	file, ok := writer.(*os.File)
	if !ok {
		return label
	}
	var stream string
	switch file {
	case os.Stdout:
		stream = "BMCSHELL_BRIDGE_COLOR_STDOUT"
	case os.Stderr:
		stream = "BMCSHELL_BRIDGE_COLOR_STDERR"
	default:
		return label
	}
	// The Bash adapter sees the actual terminal even when MSYS exposes it
	// to the native Windows executable as a pipe.
	if os.Getenv(stream) != "1" {
		return label
	}
	return style + label + "\x1b[0m"
}

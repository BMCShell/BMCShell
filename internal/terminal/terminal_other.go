//go:build !darwin && !linux && !windows

package terminal

import "os"

func open(_, _ *os.File) *Terminal { return nil }

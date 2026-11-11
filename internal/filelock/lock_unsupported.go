//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package filelock

import (
	"errors"
	"os"
)

func lock(*os.File) error {
	return errors.New("operating system does not support the BMCShell mutation lock")
}

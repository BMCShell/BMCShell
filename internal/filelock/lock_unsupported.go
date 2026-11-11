//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package filelock

import (
	"errors"
	"os"
)

func lock(*os.File) error {
	return errors.New("当前操作系统不支持 BMCShell 写入锁")
}

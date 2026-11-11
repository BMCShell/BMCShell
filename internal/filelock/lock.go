package filelock

import (
	"errors"
	"os"
)

var ErrBusy = errors.New("锁正被占用")

// Acquire holds an exclusive, nonblocking OS lock until the caller releases
// it or the process exits. Leave the lock file in place so every process
// opens the same file, even while ownership changes.
func Acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

//go:build !windows

package util

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func IsFileBusy(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EBUSY)
}

func ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

package util

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

func IsFileBusy(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, errorSharingViolation) || errors.Is(err, errorLockViolation)
}

// os.ReadFile은 FILE_SHARE_DELETE 없이 열어서, 읽는 동안 Claude Code가 크리덴셜·계정 파일을 지우거나 다른 이름으로 옮기려 하면 그쪽이 실패한다.
func ReadFile(path string) ([]byte, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ, share, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return io.ReadAll(file)
}

package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 여러 수집기가 동시에 찍어도 줄이 섞이지 않게 한 줄씩 차례로 쓴다.
var logState struct {
	sync.Mutex
	dir string
	// 파일에 쓰지 못한다는 알림을 줄마다 되풀이하지 않는다.
	failing bool
}

// 이 뒤의 로그는 콘솔과 함께 dir/YYYY-MM-DD.log에도 남는다.
func LogToDir(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	logState.Lock()
	logState.dir = dir
	logState.Unlock()
	return nil
}

func Logf(format string, args ...any) {
	now := time.Now()
	stamp := "[" + LocalDay(now) + " " + Clock(now) + "] "
	line := stamp + fmt.Sprintf(format, args...)
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	logState.Lock()
	defer logState.Unlock()
	os.Stdout.WriteString(line)
	if logState.dir == "" {
		return
	}
	err := appendLine(filepath.Join(logState.dir, LocalDay(now)+".log"), line)
	if err != nil && !logState.failing {
		os.Stdout.WriteString(stamp + "로그 파일에 쓰지 못했습니다: " + err.Error() + "\n")
	}
	logState.failing = err != nil
}

// 줄마다 열고 닫아, 서버가 도는 동안에도 로그 파일을 지우거나 옮길 수 있게 한다.
func appendLine(file, line string) error {
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

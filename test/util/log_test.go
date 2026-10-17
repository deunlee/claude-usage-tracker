//go:build separatetest

package util

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogfAlsoAppendsToDailyFile(t *testing.T) {
	dir := t.TempDir()
	if err := LogToDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		logState.Lock()
		logState.dir = ""
		logState.Unlock()
	})
	Logf("first %d", 1)
	Logf("second\n")

	files, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(files) != 1 {
		t.Fatalf("log files %v", files)
	}
	text, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	day := strings.TrimSuffix(filepath.Base(files[0]), ".log")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "["+day+" ") || !strings.HasSuffix(lines[0], "] first 1") || !strings.HasSuffix(lines[1], "] second") {
		t.Errorf("%s:\n%s", files[0], text)
	}
}

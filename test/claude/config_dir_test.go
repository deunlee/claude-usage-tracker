//go:build separatetest

package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverConfigDirNames(t *testing.T) {
	home := t.TempDir()
	for _, c := range []struct {
		name  string
		files []string
	}{
		{".claude1", []string{".claude.json", ".credentials.json"}},
		{".claude-work", []string{".claude.json", ".credentials.json"}},
		// 수집할 수 없는 폴더와, 적지 않아도 수집하거나 이름이 다른 폴더는 넣지 않는다.
		{".claude2", []string{".claude.json"}},
		{".claude3", []string{".credentials.json"}},
		{".claude4", nil},
		{".claude", []string{".claude.json", ".credentials.json"}},
		{"claude5", []string{".claude.json", ".credentials.json"}},
	} {
		dir := filepath.Join(home, c.name)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		for _, name := range c.files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 계정 정보 파일과 이름이 같은 홈의 파일을 폴더로 읽지 않는지 함께 본다.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if names := DiscoverConfigDirNames(home); !slices.Equal(names, []string{".claude-work", ".claude1"}) {
		t.Errorf("DiscoverConfigDirNames = %v", names)
	}
	if names := DiscoverConfigDirNames(filepath.Join(home, "없는폴더")); names != nil {
		t.Errorf("없는 홈에서 %v", names)
	}
}

func TestChildEnvPointsClaudeAtTheConfigDir(t *testing.T) {
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/elsewhere", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/secure"}
	explicit := ExplicitConfigDir("/custom", "/home").ChildEnv(base)
	if strings.Join(explicit, ";") != "PATH=/bin;CLAUDE_CONFIG_DIR=/custom" {
		t.Errorf("explicit env %v", explicit)
	}
	defaults := DefaultConfigDir("/home").ChildEnv(base)
	if strings.Join(defaults, ";") != "PATH=/bin" {
		t.Errorf("default env %v", defaults)
	}
}

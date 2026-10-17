//go:build separatetest

package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"claudeusage/internal/claude"
)

func TestClaudeConfigDirsSkipsMissingDirs(t *testing.T) {
	home := t.TempDir()
	existing := mkdir(t, home, ".claude2")
	missing := filepath.Join(home, ".claude3")

	dirs := claudeConfigDirs(home, []string{existing, missing})
	if got, want := configDirPaths(dirs), []string{existing}; !slices.Equal(got, want) {
		t.Fatalf("dirs = %v, want %v", got, want)
	}
}

func TestClaudeConfigDirsKeepsExistingDefaultFirst(t *testing.T) {
	home := t.TempDir()
	defaultDir := mkdir(t, home, ".claude")
	extra := mkdir(t, home, ".claude2")

	dirs := claudeConfigDirs(home, []string{extra})
	if got, want := configDirPaths(dirs), []string{defaultDir, extra}; !slices.Equal(got, want) {
		t.Fatalf("dirs = %v, want %v", got, want)
	}
	if dirs[0].AccountFile != filepath.Join(home, ".claude.json") {
		t.Fatalf("default account file = %s", dirs[0].AccountFile)
	}
}

func TestClaudeConfigDirsEmptyWhenNothingExists(t *testing.T) {
	home := t.TempDir()

	if dirs := claudeConfigDirs(home, []string{filepath.Join(home, ".claude1")}); len(dirs) != 0 {
		t.Fatalf("dirs = %v, want none", configDirPaths(dirs))
	}
}

func TestClaudeConfigDirsSkipsFileNamedLikeDir(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if dirs := claudeConfigDirs(home, nil); len(dirs) != 0 {
		t.Fatalf("dirs = %v, want none", configDirPaths(dirs))
	}
}

func mkdir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func configDirPaths(dirs []*claude.ConfigDir) []string {
	paths := make([]string, len(dirs))
	for i, dir := range dirs {
		paths[i] = dir.Dir
	}
	return paths
}

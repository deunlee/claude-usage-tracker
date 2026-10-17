//go:build separatetest

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"claudeusage/internal/util"
)

func TestLoadConfigDefaultsWhenMissing(t *testing.T) {
	config, err := loadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if config.dashboard.host != "127.0.0.1" || config.dashboard.port != 8788 || len(config.extraClaudeConfigDirs) != 0 ||
		len(config.dashboard.ipAllowList) != 0 || len(config.dashboard.trustedProxies) != 0 {
		t.Errorf("config %+v", config)
	}
}

func TestLoadConfigReadsBOMAndExpandsPaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	text := "\uFEFF" + `{"dashboard": {"port": 9000, "host": null}, "extraClaudeConfigDirs": ["~/.claude2", "relative"]}`
	if err := os.WriteFile(file, []byte(text), 0o666); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := []string{filepath.Join(home, ".claude2"), filepath.Join(dir, "relative")}
	if config.dashboard.host != "127.0.0.1" || config.dashboard.port != 9000 ||
		len(config.extraClaudeConfigDirs) != 2 || config.extraClaudeConfigDirs[0] != want[0] || config.extraClaudeConfigDirs[1] != want[1] {
		t.Errorf("config %+v, want dirs %v", config, want)
	}
}

func TestCreateConfigWritesDefaultsWithPassword(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	created, err := createConfig(file, t.TempDir())
	if err != nil || !created {
		t.Fatalf("createConfig = %v, %v", created, err)
	}
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"extraClaudeConfigDirs": []`, `"accountAliases": {}`, `"timezone": "Asia/Seoul"`, "\n    \"dashboard\": {", "\"ipAllowList\": [\n            \"127.0.0.1\"\n        ]", `"trustedProxies": []`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("config.json has no %q:\n%s", want, text)
		}
	}
	config, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if config.dashboard.host != "127.0.0.1" || config.dashboard.port != 8788 || len(config.dashboard.password) != 32 ||
		fmt.Sprint(config.dashboard.ipAllowList) != "[127.0.0.1/32]" || len(config.dashboard.trustedProxies) != 0 {
		t.Errorf("config %+v", config)
	}
	for _, c := range config.dashboard.password {
		if !strings.ContainsRune(passwordChars, c) {
			t.Errorf("password %q has %q", config.dashboard.password, c)
		}
	}
}

// 경로를 ~로 적어, 홈이 다른 곳에 있는 PC로 config.json을 옮겨도 그대로 읽히게 한다.
func TestCreateConfigWritesFoundConfigDirs(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude9")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".claude.json", ".credentials.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(t.TempDir(), "config.json")
	if created, err := createConfig(file, home); err != nil || !created {
		t.Fatalf("createConfig = %v, %v", created, err)
	}
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var content configFile
	if err := json.Unmarshal(text, &content); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(content.ExtraClaudeConfigDirs, []string{"~/.claude9"}) {
		t.Errorf("extraClaudeConfigDirs %v", content.ExtraClaudeConfigDirs)
	}
}

func TestCreateConfigKeepsExistingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o666); err != nil {
		t.Fatal(err)
	}
	if created, err := createConfig(file, t.TempDir()); err != nil || created {
		t.Fatalf("createConfig = %v, %v", created, err)
	}
	if text, _ := os.ReadFile(file); string(text) != `{}` {
		t.Errorf("config.json = %q", text)
	}
}

func TestRandomPasswordsDiffer(t *testing.T) {
	if a, b := randomPassword(), randomPassword(); a == b {
		t.Errorf("same password twice: %q", a)
	}
}

func TestParseConfigPassword(t *testing.T) {
	config, err := parseConfig(map[string]any{"dashboard": map[string]any{"password": "secret"}}, ".")
	if err != nil || config.dashboard.password != "secret" {
		t.Errorf("config %+v, %v", config, err)
	}
	config, err = parseConfig(map[string]any{"dashboard": map[string]any{"password": nil}}, ".")
	if err != nil || config.dashboard.password != "" {
		t.Errorf("config %+v, %v", config, err)
	}
}

func TestParseConfigRejectsMistakes(t *testing.T) {
	for _, c := range []struct {
		content any
		message string
	}{
		{[]any{}, "최상위 값이 객체가 아닙니다"},
		{map[string]any{"dashboard": map[string]any{"host": ""}}, "dashboard.host는 비어 있지 않은 문자열이어야 합니다"},
		{map[string]any{"dashboard": map[string]any{"port": 80.5}}, "dashboard.port는 1~65535 사이의 정수여야 합니다"},
		{map[string]any{"dashboard": map[string]any{"port": 70000.0}}, "dashboard.port는 1~65535 사이의 정수여야 합니다"},
		{map[string]any{"dashboard": map[string]any{"password": 1234.0}}, "dashboard.password는 문자열이어야 합니다"},
		{map[string]any{"dashboard": map[string]any{"ipAllowList": "192.168.0.1"}}, "dashboard.ipAllowList는 IP나 CIDR 문자열의 배열이어야 합니다"},
		{map[string]any{"dashboard": map[string]any{"ipAllowList": []any{1.0}}}, "dashboard.ipAllowList는 IP나 CIDR 문자열의 배열이어야 합니다"},
		{map[string]any{"dashboard": map[string]any{"ipAllowList": []any{"192.168.0.256"}}}, `dashboard.ipAllowList에 IP나 CIDR로 읽을 수 없는 값이 있습니다: "192.168.0.256"`},
		{map[string]any{"dashboard": map[string]any{"ipAllowList": []any{"192.168.0.0/33"}}}, "dashboard.ipAllowList에 IP나 CIDR로 읽을 수 없는 값이 있습니다"},
		{map[string]any{"dashboard": map[string]any{"ipAllowList": []any{"localhost"}}}, "dashboard.ipAllowList에 IP나 CIDR로 읽을 수 없는 값이 있습니다"},
		{map[string]any{"dashboard": map[string]any{"trustedProxies": []any{""}}}, "dashboard.trustedProxies에 IP나 CIDR로 읽을 수 없는 값이 있습니다"},
		{map[string]any{"extraClaudeConfigDirs": []any{""}}, "extraClaudeConfigDirs는 경로 문자열의 배열이어야 합니다"},
		{map[string]any{"extraClaudeConfigDirs": "dir"}, "extraClaudeConfigDirs는 경로 문자열의 배열이어야 합니다"},
		{map[string]any{"accountAliases": []any{}}, "accountAliases는 이메일을 키로"},
		{map[string]any{"accountAliases": map[string]any{"a@example.com": ""}}, "비어 있지 않은 별칭 문자열"},
		{map[string]any{"accountAliases": map[string]any{"a@example.com": 1.0}}, "비어 있지 않은 별칭 문자열"},
		{map[string]any{"accountAliases": map[string]any{" ": "x"}}, "비어 있지 않은 별칭 문자열"},
		{map[string]any{"accountAliases": map[string]any{"A@example.com": "x", "a@example.com": "y"}}, "대소문자만 다른 같은 이메일"},
		{map[string]any{"timezone": " "}, "timezone은 비어 있지 않은 IANA 시간대 이름이어야 합니다"},
		{map[string]any{"timezone": 9.0}, "timezone은 비어 있지 않은 IANA 시간대 이름이어야 합니다"},
		{map[string]any{"timezone": "Asia/Seoull"}, "timezone을 읽지 못했습니다"},
	} {
		_, err := parseConfig(c.content, ".")
		if err == nil || !strings.Contains(err.Error(), c.message) {
			t.Errorf("parseConfig(%v) = %v, want %q", c.content, err, c.message)
		}
	}
}

func TestParseConfigAccountAliases(t *testing.T) {
	config, err := parseConfig(map[string]any{"accountAliases": map[string]any{" Me@Example.com ": " 개인 ", "work@example.com": "회사"}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.accountAliases) != 2 || config.accountAliases["me@example.com"] != "개인" || config.accountAliases["work@example.com"] != "회사" {
		t.Errorf("aliases %v", config.accountAliases)
	}
}

// 오타가 있어도 나머지 설정으로 시작해야 서버가 멈추지 않는다.
func TestParseConfigIgnoresUnknownKeys(t *testing.T) {
	config, err := parseConfig(map[string]any{
		"dashbord":  map[string]any{},
		"extra":     1.0,
		"dashboard": map[string]any{"prot": 1.0, "port": 9000.0},
	}, ".")
	if err != nil || config.dashboard.port != 9000 || config.dashboard.host != "127.0.0.1" {
		t.Errorf("config %+v, %v", config, err)
	}
}

func TestParseConfigTimezone(t *testing.T) {
	config, err := parseConfig(map[string]any{}, ".")
	if err != nil || config.location.String() != "Asia/Seoul" {
		t.Errorf("timezone 없이 parseConfig = %v, %v", config.location, err)
	}
	config, err = parseConfig(map[string]any{"timezone": " America/New_York "}, ".")
	if err != nil || config.location.String() != "America/New_York" {
		t.Fatalf("parseConfig = %v, %v", config.location, err)
	}

	// 날짜 경계가 머신 시간대가 아니라 설정한 시간대에서 잘려야 잔디의 하루가 맞는다.
	defer util.SetLocation(time.Local)
	util.SetLocation(config.location)
	if day := util.LocalDay(time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)); day != "2026-10-16" {
		t.Errorf("LocalDay = %s", day)
	}
}

func TestParseConfigIPLists(t *testing.T) {
	config, err := parseConfig(map[string]any{"dashboard": map[string]any{
		"ipAllowList":    []any{" 192.168.0.10 ", "10.1.2.3/8", "::ffff:203.0.113.9", "fe80::1%eth0", "2001:db8::/32"},
		"trustedProxies": []any{},
	}}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(config.dashboard.ipAllowList); got != "[192.168.0.10/32 10.0.0.0/8 203.0.113.9/32 fe80::1/128 2001:db8::/32]" {
		t.Errorf("ipAllowList %s", got)
	}
	if config.dashboard.trustedProxies == nil || len(config.dashboard.trustedProxies) != 0 {
		t.Errorf("trustedProxies %v", config.dashboard.trustedProxies)
	}
}

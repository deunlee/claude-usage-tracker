package app

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/util"
)

type dashboardConfig struct {
	host string
	port int
	// 비어 있으면 로그인 창에서 아무것도 입력하지 않고 로그인한다.
	password       string
	ipAllowList    []netip.Prefix
	trustedProxies []netip.Prefix
}

type appConfig struct {
	dashboard dashboardConfig
	// ~/.claude는 있으면 따로 수집하므로 여기에는 그 밖의 CLAUDE_CONFIG_DIR만 둔다.
	extraClaudeConfigDirs []string
	accountAliases        claude.AccountAliases
	location              *time.Location
}

func defaultConfig() appConfig {
	return appConfig{
		// 따로 정하지 않으면 대시보드가 이 PC에서만 열리게 루프백에만 묶는다.
		dashboard: dashboardConfig{host: "127.0.0.1", port: 8788},
		location:  defaultLocation(),
	}
}

// 서버를 UTC로 도는 리눅스에 두어도 날짜가 한국 날짜로 묶이게 머신 시간대를 따르지 않고 이 값을 기본으로 쓴다.
const defaultTimezone = "Asia/Seoul"

// 시간대 데이터는 time/tzdata로 실행 파일에 들어 있어(main.go) 못 읽을 일이 없지만, nil을 돌려주면 시각을 적을 때마다 멈춘다.
func defaultLocation() *time.Location {
	location, err := time.LoadLocation(defaultTimezone)
	if err != nil {
		return time.FixedZone("KST", 9*60*60)
	}
	return location
}

const passwordLength = 32
const passwordChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// 처음 실행할 때 고칠 수 있는 키가 무엇인지 보이도록 빈 값까지 모두 적는다. 키 순서는 README의 예시와 같다.
type configFile struct {
	Dashboard struct {
		Host           string   `json:"host"`
		Port           int      `json:"port"`
		Password       string   `json:"password"`
		IPAllowList    []string `json:"ipAllowList"`
		TrustedProxies []string `json:"trustedProxies"`
	} `json:"dashboard"`
	ExtraClaudeConfigDirs []string          `json:"extraClaudeConfigDirs"`
	AccountAliases        map[string]string `json:"accountAliases"`
	Timezone              string            `json:"timezone"`
}

// 파일이 이미 있으면 아무것도 하지 않고 false를 돌려준다.
func createConfig(file, home string) (bool, error) {
	defaults := defaultConfig()
	var content configFile
	content.Dashboard.Host = defaults.dashboard.host
	content.Dashboard.Port = defaults.dashboard.port
	content.Dashboard.Password = randomPassword()
	// 대시보드를 띄운 PC에서는 로그인 없이 열리게 넣어 둔다. 키를 빼면 어느 IP도 로그인을 건너뛰지 않는다.
	content.Dashboard.IPAllowList = []string{"127.0.0.1"}
	content.Dashboard.TrustedProxies = []string{}
	// 계정을 .claude1, .claude2로 나눠 쓰는 경우가 흔해, 처음 만들 때만 찾아 적어 둔다.
	// 그 뒤로는 사용자가 지운 폴더가 다시 살아나지 않게 이 파일을 그대로 쓴다.
	content.ExtraClaudeConfigDirs = []string{}
	for _, name := range claude.DiscoverConfigDirNames(home) {
		content.ExtraClaudeConfigDirs = append(content.ExtraClaudeConfigDirs, "~/"+name)
	}
	content.AccountAliases = map[string]string{}
	content.Timezone = defaultTimezone
	text, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		return false, err
	}
	// 비밀번호가 들어 있어 다른 사용자가 읽지 못하게 하고, 확인한 뒤 그사이 생긴 파일은 덮어쓰지 않는다.
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(append(text, '\n')); err != nil {
		f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	// 파일을 만들었을 때만 알린다. 이미 있으면 위에서 돌아간다.
	if len(content.ExtraClaudeConfigDirs) > 0 {
		util.Logf("홈에서 찾은 Claude Code 설정 디렉터리를 extraClaudeConfigDirs에 넣었습니다: %s", strings.Join(content.ExtraClaudeConfigDirs, ", "))
	}
	return true, nil
}

func randomPassword() string {
	// 256이 글자 수의 배수가 아니라 나머지를 그대로 쓰면 앞쪽 글자가 더 자주 나오므로, 배수를 넘는 바이트는 버린다.
	limit := 256 / len(passwordChars) * len(passwordChars)
	password := make([]byte, 0, passwordLength)
	buf := make([]byte, passwordLength*2)
	for len(password) < passwordLength {
		rand.Read(buf)
		for _, b := range buf {
			if int(b) < limit && len(password) < passwordLength {
				password = append(password, passwordChars[int(b)%len(passwordChars)])
			}
		}
	}
	return string(password)
}

func loadConfig(file string) (appConfig, error) {
	text, err := util.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return appConfig{}, err
	}
	// 메모장이나 PowerShell 5의 Set-Content -Encoding utf8로 저장하면 BOM이 붙는데, encoding/json은 BOM을 받지 않는다.
	text = bytes.TrimPrefix(text, []byte("\uFEFF"))
	var content any
	if err := json.Unmarshal(text, &content); err != nil {
		return appConfig{}, fmt.Errorf("%s: %w", file, err)
	}
	config, err := parseConfig(content, filepath.Dir(file))
	if err != nil {
		return appConfig{}, fmt.Errorf("%s: %w", file, err)
	}
	return config, nil
}

func parseConfig(content any, baseDir string) (appConfig, error) {
	config := defaultConfig()
	root, err := section(content, "최상위", "dashboard", "extraClaudeConfigDirs", "accountAliases", "timezone")
	if err != nil {
		return config, err
	}
	dashboardValue := root["dashboard"]
	if dashboardValue == nil {
		dashboardValue = map[string]any{}
	}
	dashboard, err := section(dashboardValue, "dashboard", "host", "port", "password", "ipAllowList", "trustedProxies")
	if err != nil {
		return config, err
	}

	if value := dashboard["host"]; value != nil {
		host, ok := value.(string)
		if !ok || host == "" {
			return config, errors.New("dashboard.host는 비어 있지 않은 문자열이어야 합니다")
		}
		config.dashboard.host = host
	}
	if value := dashboard["port"]; value != nil {
		port, ok := value.(float64)
		if !ok || port != math.Trunc(port) || port < 1 || port > 65535 {
			return config, errors.New("dashboard.port는 1~65535 사이의 정수여야 합니다")
		}
		config.dashboard.port = int(port)
	}
	if value := dashboard["password"]; value != nil {
		password, ok := value.(string)
		if !ok {
			return config, errors.New("dashboard.password는 문자열이어야 합니다")
		}
		config.dashboard.password = password
	}
	if value := dashboard["ipAllowList"]; value != nil {
		if config.dashboard.ipAllowList, err = parseIPList(value, "dashboard.ipAllowList"); err != nil {
			return config, err
		}
	}
	if value := dashboard["trustedProxies"]; value != nil {
		if config.dashboard.trustedProxies, err = parseIPList(value, "dashboard.trustedProxies"); err != nil {
			return config, err
		}
	}
	if value := root["extraClaudeConfigDirs"]; value != nil {
		list, ok := value.([]any)
		if !ok {
			return config, errors.New("extraClaudeConfigDirs는 경로 문자열의 배열이어야 합니다")
		}
		for _, item := range list {
			dir, ok := item.(string)
			if !ok || dir == "" {
				return config, errors.New("extraClaudeConfigDirs는 경로 문자열의 배열이어야 합니다")
			}
			expanded, err := expandPath(dir, baseDir)
			if err != nil {
				return config, err
			}
			config.extraClaudeConfigDirs = append(config.extraClaudeConfigDirs, expanded)
		}
	}
	if value := root["accountAliases"]; value != nil {
		aliases, err := parseAccountAliases(value)
		if err != nil {
			return config, err
		}
		config.accountAliases = aliases
	}
	if value := root["timezone"]; value != nil {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return config, errors.New("timezone은 비어 있지 않은 IANA 시간대 이름이어야 합니다 (예: Asia/Seoul)")
		}
		location, err := time.LoadLocation(strings.TrimSpace(name))
		if err != nil {
			return config, fmt.Errorf("timezone을 읽지 못했습니다: %w", err)
		}
		config.location = location
	}
	return config, nil
}

// IP 하나는 그 주소만 드는 대역으로 바꿔, 받은 연결의 주소를 대역 검사 한 가지로 확인한다.
func parseIPList(value any, name string) ([]netip.Prefix, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s는 IP나 CIDR 문자열의 배열이어야 합니다", name)
	}
	prefixes := make([]netip.Prefix, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s는 IP나 CIDR 문자열의 배열이어야 합니다", name)
		}
		text = strings.TrimSpace(text)
		var prefix netip.Prefix
		var err error
		if strings.Contains(text, "/") {
			prefix, err = netip.ParsePrefix(text)
		} else {
			var addr netip.Addr
			if addr, err = netip.ParseAddr(text); err == nil {
				addr = addr.Unmap().WithZone("")
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s에 IP나 CIDR로 읽을 수 없는 값이 있습니다: %q", name, text)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// claude.AccountAliases.Lookup이 소문자로 찾으므로 키를 소문자로 맞춰 둔다.
func parseAccountAliases(value any) (claude.AccountAliases, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("accountAliases는 이메일을 키로, 별칭을 값으로 하는 객체여야 합니다")
	}
	aliases := make(claude.AccountAliases, len(object))
	for email, raw := range object {
		alias, ok := raw.(string)
		if strings.TrimSpace(email) == "" || !ok || strings.TrimSpace(alias) == "" {
			return nil, errors.New("accountAliases는 이메일을 키로, 비어 있지 않은 별칭 문자열을 값으로 하는 객체여야 합니다")
		}
		key := strings.ToLower(strings.TrimSpace(email))
		if _, dup := aliases[key]; dup {
			return nil, fmt.Errorf("accountAliases에 대소문자만 다른 같은 이메일이 있습니다: %s", key)
		}
		aliases[key] = strings.TrimSpace(alias)
	}
	return aliases, nil
}

// 키 이름을 잘못 적었을 때 조용히 기본값으로 도는 일이 없도록 모르는 키를 알린다.
// 서버로 돌릴 때 오타 하나로 시작하지 못하면 곤란하므로 막지는 않는다.
func section(value any, name string, keys ...string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 값이 객체가 아닙니다", name)
	}
	var unknown []string
	for key := range object {
		if !slices.Contains(keys, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		util.Logf("경고: %s에 알 수 없는 키가 있어 무시합니다: %s", name, strings.Join(unknown, ", "))
	}
	return object, nil
}

func expandPath(path, baseDir string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home + path[1:]
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Join(baseDir, path), nil
}

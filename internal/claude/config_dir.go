package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"claudeusage/internal/util"
)

type ConfigDir struct {
	Dir         string
	AccountFile string
	Credentials CredentialStore
	explicit    bool
}

func newClaudeConfigDir(dir string, explicit bool, home string) *ConfigDir {
	// Claude Code는 CLAUDE_CONFIG_DIR 없이 실행됐을 때만 계정 정보 파일을 디렉터리 밖(홈의 .claude.json)에 둔다.
	accountFile := filepath.Join(home, ".claude.json")
	if explicit {
		accountFile = filepath.Join(dir, ".claude.json")
	}
	return &ConfigDir{
		Dir:         dir,
		AccountFile: accountFile,
		Credentials: CredentialStore{path: filepath.Join(dir, ".credentials.json")},
		explicit:    explicit,
	}
}

func DefaultConfigDir(home string) *ConfigDir {
	return newClaudeConfigDir(filepath.Join(home, ".claude"), false, home)
}

func ExplicitConfigDir(dir, home string) *ConfigDir {
	return newClaudeConfigDir(dir, true, home)
}

const configDirPrefix = ".claude"

// config.json을 처음 만들 때 extraClaudeConfigDirs에 넣을 홈 아래 폴더 이름을 돌려준다.
// 계정마다 CLAUDE_CONFIG_DIR를 따로 쓸 때 .claude1이나 .claude-work처럼 .claude로 시작하는 이름을 쓰므로 그 폴더를 모두 보고,
// 수집에 계정 정보와 크리덴셜이 모두 필요하니 둘 다 있는 것만 넣는다. ~/.claude는 적지 않아도 수집하므로 뺀다.
// os.ReadDir가 이름 순으로 돌려주어 따로 줄 세우지 않는다.
func DiscoverConfigDirNames(home string) []string {
	entries, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if name == configDirPrefix || !strings.HasPrefix(name, configDirPrefix) {
			continue
		}
		dir := filepath.Join(home, name)
		if !fileExists(filepath.Join(dir, ".claude.json")) || !fileExists(filepath.Join(dir, ".credentials.json")) {
			continue
		}
		names = append(names, name)
	}
	return names
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

type Account struct {
	Email string
	// 플랜 이름(Ultra 등)은 서버가 프로필로 정해 주고, Claude Code도 크리덴셜의 subscriptionType·rateLimitTier보다 이 이름을 먼저 보여 준다.
	// Claude Code가 프로필을 아직 받지 않았거나 이 필드를 적지 않던 버전이면 비어 있다.
	PlanDisplayName string
}

func (c *ConfigDir) Account() (Account, error) {
	text, err := util.ReadFile(c.AccountFile)
	if err != nil {
		return Account{}, err
	}
	var content any
	if err := json.Unmarshal(text, &content); err != nil {
		return Account{}, fmt.Errorf("%s: %w", c.AccountFile, err)
	}
	root, _ := content.(map[string]any)
	oauthAccount, _ := root["oauthAccount"].(map[string]any)
	email, _ := oauthAccount["emailAddress"].(string)
	if email == "" {
		return Account{}, fmt.Errorf("%s에 oauthAccount.emailAddress가 없습니다", c.AccountFile)
	}
	plan, _ := oauthAccount["planDisplayName"].(string)
	return Account{Email: email, PlanDisplayName: plan}, nil
}

func (c *ConfigDir) Email() (string, error) {
	account, err := c.Account()
	return account.Email, err
}

// 토큰 갱신에 띄우는 claude가 서버를 띄운 셸의 설정이 아니라 이 디렉터리의 크리덴셜을 갱신하게 한다.
func (c *ConfigDir) ChildEnv(base []string) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		// CLAUDE_SECURESTORAGE_CONFIG_DIR가 있으면 Claude Code는 CLAUDE_CONFIG_DIR와 상관없이 그쪽 크리덴셜을 쓴다.
		if envNameIs(name, "CLAUDE_SECURESTORAGE_CONFIG_DIR") || envNameIs(name, "CLAUDE_CONFIG_DIR") {
			continue
		}
		env = append(env, entry)
	}
	if c.explicit {
		env = append(env, "CLAUDE_CONFIG_DIR="+c.Dir)
	}
	return env
}

// Windows의 환경 변수 이름은 대소문자를 가리지 않는다.
func envNameIs(name, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, want)
	}
	return name == want
}

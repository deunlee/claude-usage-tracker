package app

import (
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"claudeusage/internal/claude"
	"claudeusage/internal/dashboard"
	"claudeusage/internal/poller"
	"claudeusage/internal/store"
	"claudeusage/internal/util"
)

func Run() int {
	startedAt := time.Now()
	executable, err := os.Executable()
	if err != nil {
		util.Logf("실행 파일 위치를 알 수 없습니다: %v", err)
		return 1
	}
	root := filepath.Dir(executable)
	logDir := filepath.Join(root, "logs")
	if err := util.LogToDir(logDir); err != nil {
		util.Logf("로그 폴더를 만들지 못해 콘솔에만 남깁니다: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		util.Logf("홈 디렉터리를 알 수 없습니다: %v", err)
		return 1
	}
	archiveDir := filepath.Join(root, "archive")

	configPath := filepath.Join(root, "config.json")
	created, err := createConfig(configPath, home)
	if err != nil {
		util.Logf("설정 파일을 만들지 못했습니다: %v", err)
		return 1
	}
	if created {
		// 로그 파일에도 남으므로 비밀번호는 찍지 않는다.
		util.Logf("설정 파일이 없어 새로 만들었습니다. 대시보드 비밀번호는 이 파일의 dashboard.password에 있습니다: %s", configPath)
	}
	config, err := loadConfig(configPath)
	if err != nil {
		util.Logf("설정을 읽지 못했습니다: %v", err)
		return 1
	}
	// 수집기와 대시보드가 돌기 전에 끝낸다. 이 앞의 로그 몇 줄은 머신 시간대로 찍힌다.
	util.SetLocation(config.location)
	// 비밀번호 키가 생기기 전에 만든 config.json은 키가 없어 비밀번호 없이 열리는데, 그 사실을 알아챌 곳이 따로 없다.
	if config.dashboard.password == "" {
		util.Logf("경고: dashboard.password가 비어 있어 대시보드에 비밀번호 없이 로그인할 수 있습니다: %s", configPath)
	}
	if len(config.dashboard.ipAllowList) > 0 {
		util.Logf("비밀번호 없이 대시보드를 여는 IP: %s", joinPrefixes(config.dashboard.ipAllowList))
	}
	configDirs := claudeConfigDirs(home, config.extraClaudeConfigDirs)
	if len(configDirs) == 0 {
		util.Logf("수집할 계정이 없어 종료합니다. ~/.claude도, extraClaudeConfigDirs에 적은 폴더도 없습니다")
		return 0
	}
	archive := store.NewArchive(archiveDir, logDir)
	if err := archive.Init(); err != nil {
		util.Logf("아카이브 디렉터리를 만들지 못했습니다: %v", err)
		return 1
	}

	client := claude.NewClient()
	pollers := make([]*poller.Poller, len(configDirs))
	for i, configDir := range configDirs {
		refresher := &claude.TokenRefresher{Env: configDir.ChildEnv(os.Environ())}
		pollers[i] = poller.New(configDir, client, archive, refresher, config.accountAliases)
	}
	index := store.NewIndex(archiveDir, poller.BaseInterval)
	access := dashboard.Access{Password: config.dashboard.password, AllowedIPs: config.dashboard.ipAllowList, TrustedProxies: config.dashboard.trustedProxies}
	server := dashboard.New(config.dashboard.host, config.dashboard.port, access, index, pollers, config.accountAliases)

	for _, configDir := range configDirs {
		if email, err := configDir.Email(); err != nil {
			util.Logf("수집 대상: %s (이메일을 읽지 못함: %v)", configDir.Dir, err)
		} else {
			util.Logf("수집 대상: %s (%s)", email, configDir.Dir)
		}
	}
	util.Logf("아카이브: %s", archiveDir)
	util.Logf("시간대: %s (UTC%s)", config.location, util.Local(time.Now()).Format("-07:00"))
	emails := accountEmails(configDirs)
	recordMissedResets(archive, emails, config.accountAliases, startedAt)
	// 꺼져 있던 동안의 초기화 기록이 들어간 뒤에 집계해야 그 기록까지 센다.
	index.WarmActivity(time.Now(), emails)
	serverStopped, err := server.Listen()
	if err != nil {
		util.Logf("대시보드 서버를 열지 못했습니다: %v", err)
		return 1
	}
	util.Logf("대시보드: %s", server.URL())

	// Windows에서 콘솔 창을 닫을 때도 SIGTERM으로 들어와, 진행 중인 기록을 마칠 틈이 생긴다.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	for _, p := range pollers {
		p.Start()
	}

	exitCode := 0
	select {
	case sig := <-signals:
		name := "SIGTERM"
		if sig == os.Interrupt {
			name = "SIGINT"
		}
		util.Logf("%s — 진행 중인 수집을 마치고 종료합니다", name)
	case err := <-serverStopped:
		util.Logf("대시보드 서버가 멈췄습니다: %v — 진행 중인 수집을 마치고 종료합니다", err)
		exitCode = 1
	}
	server.Close()
	var wg sync.WaitGroup
	for _, p := range pollers {
		wg.Go(p.Stop)
	}
	wg.Wait()
	return exitCode
}

func joinPrefixes(prefixes []netip.Prefix) string {
	texts := make([]string, len(prefixes))
	for i, prefix := range prefixes {
		texts[i] = prefix.String()
		if prefix.IsSingleIP() {
			texts[i] = prefix.Addr().String()
		}
	}
	return strings.Join(texts, ", ")
}

// 첫 수집보다 먼저 해야, 꺼져 있던 동안의 초기화가 첫 수집 기록보다 앞선 시각으로 들어간다.
func recordMissedResets(archive *store.Archive, emails []string, aliases claude.AccountAliases, startedAt time.Time) {
	for _, email := range emails {
		name := email
		if alias, ok := aliases.Lookup(email); ok {
			name = alias
		}
		resets, err := archive.RecordMissedResets(email, startedAt)
		for _, reset := range resets {
			util.Logf("[%s] 꺼져 있던 동안 %s 창이 %s에 초기화돼 그 시각에 0%%로 기록했습니다", name, reset.Label, util.Local(reset.At).Format("01/02 15:04:05"))
		}
		if err != nil {
			util.Logf("[%s] 꺼져 있던 동안의 초기화를 기록하지 못했습니다: %v", name, err)
		}
	}
}

// 두 설정 디렉터리가 같은 계정에 로그인돼 있을 수 있어 겹치는 이메일은 한 번만 넣는다. 읽지 못한 계정은 뺀다.
func accountEmails(configDirs []*claude.ConfigDir) []string {
	var emails []string
	for _, configDir := range configDirs {
		if email, err := configDir.Email(); err == nil && !slices.Contains(emails, email) {
			emails = append(emails, email)
		}
	}
	return emails
}

// 계정마다 CLAUDE_CONFIG_DIR를 따로 쓰면 ~/.claude가 없을 수 있다. 없는 폴더를 수집하면 매번 실패하고,
// ~/.claude는 토큰 갱신이 CLAUDE_CONFIG_DIR 없이 claude를 띄워 새로 만들어 버린다.
func claudeConfigDirs(home string, extraDirs []string) []*claude.ConfigDir {
	var dirs []*claude.ConfigDir
	if dirExists(filepath.Join(home, ".claude")) {
		dirs = append(dirs, claude.DefaultConfigDir(home))
	}
	for _, dir := range extraDirs {
		if dirExists(dir) {
			dirs = append(dirs, claude.ExplicitConfigDir(dir, home))
		}
	}
	return dirs
}

// 권한 오류처럼 열 수 없는 폴더도 따로 알리지 않는다. 빠진 폴더는 시작할 때 찍는 수집 대상 목록으로 알 수 있다.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

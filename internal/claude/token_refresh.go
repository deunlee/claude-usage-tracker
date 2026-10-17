package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// 토큰을 직접 갱신하지 않고 Claude Code를 한 번 실행해 갱신을 맡긴다.
// 크리덴셜 파일을 쓰는 주체를 Claude Code 하나로 두어야 서로 덮어쓰지 않는다.
// 응답은 버리므로 가장 가벼운 모델로 묻고, 갱신할 때마다 ~/.claude/projects에 세션 파일이 쌓이지 않게 한다.
// -p는 폴더 신뢰를 묻지 않아, 작업 디렉터리 쪽 설정(훅·env·apiKeyHelper)·.mcp.json·CLAUDE.md를 그대로 읽는다.
// 사용자 설정만 읽고 MCP 서버를 띄우지 않게 막는다. --bare도 이것들을 건너뛰지만 OAuth 크리덴셜을 읽지 않아 갱신이 되지 않는다.
const RefreshCommand = "claude -p hello --model haiku --no-session-persistence --setting-sources user --strict-mcp-config"
const refreshTimeout = 2 * time.Minute

// 셸을 끝내도 claude는 따로 살아남아 stderr 파이프를 쥐고 있을 수 있어, 그 파이프가 닫히기를 무한정 기다리지 않는다.
const refreshPipeWait = 5 * time.Second

type RefreshRun struct {
	OK     bool
	Detail string
}

type TokenRefresher struct {
	Env []string
}

func (r *TokenRefresher) Run(ctx context.Context) RefreshRun {
	started := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	cmd := shellCommand(runCtx, RefreshCommand)
	cmd.Env = r.Env
	// 표준입력은 비워 둔다(os.DevNull). 열린 채로 남으면 -p가 입력을 더 기다릴 수 있다.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = refreshPipeWait

	err := cmd.Run()
	seconds := fmt.Sprintf("%.1f초", time.Since(started).Seconds())
	if err == nil {
		return RefreshRun{OK: true, Detail: seconds}
	}
	var reason string
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		reason = "종료 요청으로 중단"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		reason = fmt.Sprintf("%d초 안에 끝나지 않음", int(refreshTimeout.Seconds()))
	case errors.As(err, &exitErr):
		reason = fmt.Sprintf("종료 코드 %d", exitErr.ExitCode())
	default:
		reason = err.Error()
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if lastLine := strings.TrimSpace(lines[len(lines)-1]); lastLine != "" {
		reason += ": " + lastLine
	}
	return RefreshRun{OK: false, Detail: reason}
}

// npm으로 설치한 claude는 claude.cmd라 셸을 거쳐야 실행된다.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		shell := os.Getenv("ComSpec")
		if shell == "" {
			shell = "cmd.exe"
		}
		return exec.CommandContext(ctx, shell, "/d", "/s", "/c", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}

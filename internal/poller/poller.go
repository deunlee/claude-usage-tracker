package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"claudeusage/internal/claude"
	"claudeusage/internal/store"
	"claudeusage/internal/util"
)

// 만료된 토큰에는 401이 아니라 긴 retry-after가 붙은 429가 돌아온다는 보고가 있어, 만료 직전 토큰은 보내지 않는다.
const expiryMargin = 30 * time.Second

// 갱신에 실패했을 때 수집 주기마다 claude를 다시 띄우지 않도록 두는 간격.
const refreshRetry = 10 * time.Minute

// 차트는 기록 사이를 직선으로 잇는다. 5시간·주간 창이 리셋되기 직전과 직후에 한 번씩 기록해야 사선으로 내려오지 않고 리셋 시각에 떨어진다.
const preResetSample = 2 * time.Minute

// 한도에 닿으면 초기화 전까지 값이 100%에 머물러 있으므로 촘촘히 물을 필요가 없다.
const limitReachedInterval = 30 * time.Minute

// 초기화 시각은 서버 시계로 적혀 오고 타이머는 이 PC의 시계로 재므로, 딱 맞춰 깨면 시계 차이만큼 리셋 전 값을 받을 수 있다.
const wakeSlack = time.Second

const outcomeSkipped = "skipped"

const (
	pauseRateLimited  = "rate_limited"
	pauseAuthRejected = "auth_rejected"
)

var pauseLabels = map[string]string{
	pauseRateLimited:  "rate limit",
	pauseAuthRejected: "인증 거부",
}

type pause struct {
	until            time.Time
	reason           string
	tokenFingerprint string
}

type LatestSnapshot struct {
	FetchedAt string
	Snapshot  *claude.Snapshot
}

type Status struct {
	StartedAt     string         `json:"startedAt"`
	LastAttemptAt *string        `json:"lastAttemptAt"`
	LastSuccessAt *string        `json:"lastSuccessAt"`
	LastOutcome   *string        `json:"lastOutcome"`
	NextPollAt    *string        `json:"nextPollAt"`
	PausedUntil   *string        `json:"pausedUntil"`
	PauseReason   *string        `json:"pauseReason"`
	Schedule      scheduleStatus `json:"schedule"`
	LimitReached  bool           `json:"limitReached"`
	// 한도에 닿아 있으면 수집 주기 단계와 상관없이 limitReachedInterval이라, 지금 실제로 쓰는 간격을 따로 알린다.
	IntervalMs int64                    `json:"intervalMs"`
	Token      *claude.CredentialStatus `json:"token"`
	UserAgent  string                   `json:"userAgent"`
	Email      *string                  `json:"email"`
}

type Poller struct {
	configDir *claude.ConfigDir
	client    *claude.Client
	archive   *store.Archive
	refresher *claude.TokenRefresher
	aliases   claude.AccountAliases
	startedAt string
	cancel    context.CancelFunc
	done      chan struct{}
	// 대시보드 요청이 수집 고루틴의 필드를 직접 고치지 않도록, 새로고침 요청은 채널로 넘겨 수집 고루틴이 처리한다.
	refresh chan chan struct{}

	// 아래 필드는 수집 고루틴만 고친다. 고칠 때와 대시보드 요청(status, latest)이 읽을 때만 잠근다.
	mu                    sync.Mutex
	schedule              adaptiveSchedule
	pause                 *pause
	nextRefreshAt         time.Time
	lastAttemptAt         string
	lastSuccessAt         string
	lastOutcome           string
	nextPollAt            string
	token                 *claude.CredentialStatus
	latest                *LatestSnapshot
	email                 string
	emailTokenFingerprint string
	// recent.json에서 마지막 응답을 되살려 본 계정. 계정마다 한 번만 읽는다.
	restoredEmail string
}

func New(configDir *claude.ConfigDir, client *claude.Client, archive *store.Archive, refresher *claude.TokenRefresher, aliases claude.AccountAliases) *Poller {
	return &Poller{
		configDir: configDir,
		client:    client,
		archive:   archive,
		refresher: refresher,
		aliases:   aliases,
		startedAt: util.ISOString(time.Now()),
		refresh:   make(chan chan struct{}, 1),
	}
}

func (p *Poller) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go p.run(ctx)
}

// 진행 중인 수집은 끝까지 기록하고 멈춘다. 토큰 갱신에 띄운 claude만 바로 끊는다.
func (p *Poller) Stop() {
	p.cancel()
	<-p.done
}

// 마지막 수집을 시작한 지 BaseInterval이 지나면 바로 수집하고 수집 주기를 BaseInterval로 되돌리게 한다.
// 돌려준 채널은 수집 고루틴이 요청을 반영해 다음 수집 시각(Status의 NextPollAt)을 고친 뒤 닫힌다.
func (p *Poller) RequestRefresh() <-chan struct{} {
	handled := make(chan struct{})
	select {
	case p.refresh <- handled:
	default:
		// 앞선 요청이 아직 처리되지 않았으면 그 요청이 같은 일을 한다.
		close(handled)
	}
	return handled
}

func (p *Poller) LastSnapshot() *LatestSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.latest
}

func (p *Poller) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	status := Status{
		StartedAt:     p.startedAt,
		LastAttemptAt: util.Optional(p.lastAttemptAt),
		LastSuccessAt: util.Optional(p.lastSuccessAt),
		LastOutcome:   util.Optional(p.lastOutcome),
		NextPollAt:    util.Optional(p.nextPollAt),
		Schedule:      p.schedule.status(),
		LimitReached:  p.limitReached(now),
		IntervalMs:    p.interval(now).Milliseconds(),
		Token:         p.token,
		UserAgent:     p.client.UserAgent,
		Email:         util.Optional(p.email),
	}
	if p.pause != nil {
		status.PausedUntil = util.Optional(util.ISOString(p.pause.until))
		status.PauseReason = util.Optional(p.pause.reason)
	}
	return status
}

func (p *Poller) run(ctx context.Context) {
	defer close(p.done)
	for {
		started := time.Now()
		p.pollOnceGuarded(ctx)
		if ctx.Err() != nil {
			return
		}
		if !p.wait(ctx, started, p.nextPollTime(started)) {
			return
		}
	}
}

// 다음 수집 때까지 기다리다가 새로고침 요청이 오면 수집 시각을 앞당긴다. ctx가 끝나면 false를 돌려준다.
func (p *Poller) wait(ctx context.Context, started, next time.Time) bool {
	for {
		delay := max(time.Second, time.Until(next))
		p.mu.Lock()
		p.nextPollAt = util.ISOString(time.Now().Add(delay))
		p.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case handled := <-p.refresh:
			timer.Stop()
			next = p.acceptRefresh(started, next)
			// nextPollAt을 고친 뒤에 알려야 요청한 쪽이 바뀐 시각을 읽는다.
			p.mu.Lock()
			p.nextPollAt = util.ISOString(time.Now().Add(max(time.Second, time.Until(next))))
			p.mu.Unlock()
			close(handled)
		}
	}
}

func (p *Poller) acceptRefresh(started, next time.Time) time.Time {
	p.mu.Lock()
	change, changed := p.schedule.resetToBase()
	p.mu.Unlock()
	// 한도에 닿아 있어도 요청한 한 번은 BaseInterval 뒤에 수집한다. 그 뒤로는 다시 한도 도달 주기를 따른다.
	next = earlier(next, p.pollTimeAfter(started, BaseInterval))
	note := ""
	if changed {
		note = fmt.Sprintf(", 수집 주기 %s → %s", minutes(change.from), minutes(change.to))
	}
	p.logf("대시보드에서 새로고침을 요청했습니다 — %s에 수집합니다%s", util.Clock(later(next, time.Now())), note)
	return next
}

// 한 번의 수집에서 생긴 버그로 다른 계정과 대시보드까지 멈추지 않게 한다.
func (p *Poller) pollOnceGuarded(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			p.logf("예상하지 못한 오류: %v", r)
		}
	}()
	p.pollOnce(ctx)
}

func (p *Poller) nextPollTime(started time.Time) time.Time {
	return p.pollTimeAfter(started, p.interval(started))
}

func (p *Poller) pollTimeAfter(started time.Time, interval time.Duration) time.Time {
	// 시작 시각 기준으로 재야 요청이 느려도 두 요청의 도착 간격이 설정보다 짧아지지 않는다.
	// 리셋 앞뒤 기록도 rate limit을 피하려고 가장 촘촘한 주기보다 붙여 보내지 않는다.
	next := started.Add(interval)
	if p.latest != nil {
		if sampleAt, ok := nextResetSampleAt(p.latest.Snapshot, started); ok {
			next = earlier(next, later(started.Add(BaseInterval), sampleAt.Add(wakeSlack)))
		}
	}
	// 인증 거부는 토큰이 바뀌면 바로 풀리므로 주기대로 확인한다. 나머지 보류는 끝나기 전에 깨어나 봐야 건너뛸 뿐이다.
	if p.pause != nil && p.pause.reason != pauseAuthRejected {
		next = later(next, p.pause.until.Add(wakeSlack))
	}
	return next
}

func nextResetSampleAt(snapshot *claude.Snapshot, after time.Time) (time.Time, bool) {
	var next time.Time
	found := false
	for _, resetAt := range snapshot.PrimaryResetTimes() {
		for _, t := range []time.Time{resetAt.Add(-preResetSample), resetAt} {
			if t.After(after) && (!found || t.Before(next)) {
				next, found = t, true
			}
		}
	}
	return next, found
}

func (p *Poller) interval(at time.Time) time.Duration {
	if p.limitReached(at) {
		return limitReachedInterval
	}
	return p.schedule.interval()
}

func (p *Poller) limitReached(at time.Time) bool {
	return p.latest != nil && len(p.latest.Snapshot.ReachedLimits(at)) > 0
}

func (p *Poller) pollOnce(ctx context.Context) {
	p.mu.Lock()
	p.lastAttemptAt = util.ISOString(time.Now())
	p.mu.Unlock()
	credentials, err := p.configDir.Credentials.Read()
	if err != nil {
		p.skip("크리덴셜을 읽지 못했습니다: " + err.Error())
		return
	}
	p.setToken(credentials)
	email, ok := p.readEmail(credentials)
	if !ok {
		return
	}
	p.restoreLatest(email)

	if p.pause != nil {
		// 인증 거부는 그 토큰에 대한 판정이라, Claude Code가 토큰을 갱신했으면 기다리지 않고 다시 시도한다.
		tokenReplaced := p.pause.reason == pauseAuthRejected && p.pause.tokenFingerprint != credentials.Fingerprint
		if time.Now().Before(p.pause.until) && !tokenReplaced {
			p.skip(fmt.Sprintf("%s — %s까지 요청하지 않습니다", pauseLabels[p.pause.reason], util.Clock(p.pause.until)))
			return
		}
		p.setPause(nil)
	}
	if missing := credentials.MissingScopes(); len(missing) > 0 {
		p.skip(fmt.Sprintf("토큰 scope에 %s가 없습니다", strings.Join(missing, ", ")))
		return
	}
	if credentials.ExpiresWithin(expiryMargin, time.Now()) {
		refreshed := p.refreshToken(ctx)
		if refreshed == nil {
			return
		}
		credentials = refreshed
	}

	fetchedAt := time.Now()
	base := store.Record{
		FetchedAt:    util.ISOString(fetchedAt),
		CLIVersion:   claude.CodeVersion,
		NextPollInMs: p.interval(fetchedAt).Milliseconds(),
	}
	response, err := p.client.Get(credentials)
	if err != nil {
		base.Error = err.Error()
		p.record(email, fetchedAt, base, claude.OutcomeError, "요청 실패: "+err.Error(), nil)
		return
	}
	durationMs := response.Duration.Milliseconds()
	base.HTTPStatus, base.DurationMs = &response.Status, &durationMs
	p.handleResponse(email, fetchedAt, response, credentials, base)
}

// 다시 시작한 뒤 첫 수집이 rate limit 등으로 실패해도 대시보드가 0%가 아니라 마지막으로 받은 값을 보여 주게 한다.
// 수집 주기(한도 도달·초기화 전후 기록)도 그 값을 기준으로 정해진다.
func (p *Poller) restoreLatest(email string) {
	if p.latest != nil || p.restoredEmail == email {
		return
	}
	p.restoredEmail = email
	fetchedAt, snapshot, err := p.archive.LastSnapshot(email)
	if err != nil {
		p.logf("마지막 응답을 읽지 못했습니다: %v", err)
		return
	}
	if snapshot == nil {
		return
	}
	p.mu.Lock()
	p.latest = &LatestSnapshot{FetchedAt: util.ISOString(fetchedAt), Snapshot: snapshot}
	p.mu.Unlock()
}

func (p *Poller) readEmail(credentials *claude.Credentials) (string, bool) {
	email, err := p.configDir.Email()
	if err != nil {
		// Claude Code가 계정 정보 파일을 자주 고쳐 써서, 쓰는 도중에 읽으면 JSON이 깨져 보일 수 있다.
		// 다른 계정으로 로그인하면 토큰도 바뀌므로, 토큰이 그대로면 직전에 읽은 계정으로 본다.
		if p.email != "" && p.emailTokenFingerprint == credentials.Fingerprint {
			return p.email, true
		}
		p.skip("계정 정보를 읽지 못했습니다: " + err.Error())
		return "", false
	}
	switched := p.email != "" && p.email != email
	if switched {
		p.logf("이 디렉터리의 계정이 %s(으)로 바뀌었습니다", email)
	}
	p.mu.Lock()
	if switched {
		p.latest = nil
	}
	p.email, p.emailTokenFingerprint = email, credentials.Fingerprint
	p.mu.Unlock()
	return email, true
}

func (p *Poller) handleResponse(email string, fetchedAt time.Time, response *claude.Response, credentials *claude.Credentials, base store.Record) {
	outcome := claude.Classify(response)
	switch outcome.Kind {
	case claude.OutcomeOK:
		notes := p.acceptSnapshot(base.FetchedAt, fetchedAt, outcome.Snapshot)
		record := base
		record.OK, record.NextPollInMs, record.Data = true, p.interval(fetchedAt).Milliseconds(), outcome.Snapshot.Raw
		summary := store.SummaryOf(outcome.Snapshot, fetchedAt)
		p.record(email, fetchedAt, record, claude.OutcomeOK, outcome.Snapshot.Summary()+notes, &summary)
	case claude.OutcomeRateLimited:
		p.setPause(&pause{until: time.Now().Add(outcome.Wait), reason: pauseRateLimited, tokenFingerprint: credentials.Fingerprint})
		retryAfter, retryAfterText := json.RawMessage("null"), "없음"
		if values := response.Header.Values("Retry-After"); len(values) > 0 {
			retryAfter, _ = util.MarshalJSON(values[0])
			retryAfterText = values[0]
		}
		record := base
		record.Error, record.RetryAfter, record.ResponseBody = outcome.Detail, retryAfter, clipBody(response)
		p.record(email, fetchedAt, record, claude.OutcomeRateLimited,
			fmt.Sprintf("%s — rate limit, %d초 기다립니다 (retry-after: %s)", outcome.Detail, int64(math.Round(outcome.Wait.Seconds())), retryAfterText), nil)
	case claude.OutcomeAuthRejected:
		p.setPause(&pause{until: time.Now().Add(claude.MaxWait), reason: pauseAuthRejected, tokenFingerprint: credentials.Fingerprint})
		record := base
		record.Error, record.ResponseBody = outcome.Detail, clipBody(response)
		p.record(email, fetchedAt, record, claude.OutcomeAuthRejected, outcome.Detail+" — 인증 거부, 토큰이 바뀌거나 1시간이 지날 때까지 기다립니다", nil)
	default:
		record := base
		record.Error, record.ResponseBody = outcome.Detail, clipBody(response)
		p.record(email, fetchedAt, record, claude.OutcomeError, outcome.Detail, nil)
	}
}

// 받은 응답을 최신 값으로 두고 수집 주기를 고친 뒤, 로그 끝에 붙일 설명을 돌려준다.
func (p *Poller) acceptSnapshot(fetchedAtText string, fetchedAt time.Time, snapshot *claude.Snapshot) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSuccessAt = fetchedAtText
	p.latest = &LatestSnapshot{FetchedAt: fetchedAtText, Snapshot: snapshot}
	// 앞당긴 주기를 같은 응답의 '변화 없음' 판정이 다시 늦추지 않도록 observe보다 먼저 되돌린다.
	notes := p.tightenBeforeReset(snapshot, fetchedAt)
	if change, ok := p.schedule.observe(snapshot.Fingerprint(), fetchedAtText); ok {
		verdict := "변화 없음"
		if change.changed {
			verdict = "변화 감지"
		}
		notes += fmt.Sprintf(" · %s, 수집 주기 %s → %s", verdict, minutes(change.from), minutes(change.to))
	}
	return notes + limitNote(snapshot, fetchedAt)
}

func (p *Poller) tightenBeforeReset(snapshot *claude.Snapshot, at time.Time) string {
	resetSoon := false
	for _, resetAt := range snapshot.PrimaryResetTimes() {
		if resetAt.After(at) && resetAt.Sub(at) <= preResetSample {
			resetSoon = true
		}
	}
	if !resetSoon {
		return ""
	}
	change, ok := p.schedule.resetToBase()
	if !ok {
		return ""
	}
	return fmt.Sprintf(" · 초기화 %s 전, 수집 주기 %s → %s", minutes(preResetSample), minutes(change.from), minutes(change.to))
}

func limitNote(snapshot *claude.Snapshot, at time.Time) string {
	reached := snapshot.ReachedLimits(at)
	if len(reached) == 0 {
		return ""
	}
	labels := make([]string, len(reached))
	var lastReset time.Time
	for i, r := range reached {
		labels[i] = r.ShortLabel
		if r.ResetsAt != nil {
			if t, ok := util.ParseTime(*r.ResetsAt); ok {
				lastReset = later(lastReset, t)
			}
		}
	}
	// 둘 다 닿았으면 늦게 풀리는 쪽까지 이 주기가 이어진다.
	until := ""
	if !lastReset.IsZero() {
		until = fmt.Sprintf(" (초기화 %s)", util.Clock(lastReset))
	}
	return fmt.Sprintf(" · %s 한도 도달, %s마다 수집%s", strings.Join(labels, "·"), minutes(limitReachedInterval), until)
}

func (p *Poller) refreshToken(ctx context.Context) *claude.Credentials {
	if time.Now().Before(p.nextRefreshAt) {
		p.skip(fmt.Sprintf("토큰이 만료됐습니다 — %s에 '%s'로 다시 갱신해 봅니다", util.Clock(p.nextRefreshAt), claude.RefreshCommand))
		return nil
	}
	p.logf("토큰이 만료돼 '%s'를 실행해 갱신합니다", claude.RefreshCommand)
	run := p.refresher.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	var problem string
	credentials, err := p.configDir.Credentials.Read()
	if err == nil {
		p.setToken(credentials)
		// 종료 코드와 상관없이 토큰이 바뀌었으면 성공으로 본다. 한도에 막혀 hello 응답이 실패해도 갱신은 먼저 끝나 있을 수 있다.
		if !credentials.ExpiresWithin(expiryMargin, time.Now()) {
			p.logf("토큰을 갱신했습니다 ('%s' %s)", claude.RefreshCommand, run.Detail)
			return credentials
		}
		problem = run.Detail
		if run.OK {
			problem = "실행은 끝났지만 토큰이 그대로입니다"
		}
	} else {
		problem = "크리덴셜을 다시 읽지 못했습니다: " + err.Error()
	}
	p.nextRefreshAt = time.Now().Add(refreshRetry)
	p.skip(fmt.Sprintf("토큰 갱신 실패 (%s) — %s에 다시 시도합니다", problem, util.Clock(p.nextRefreshAt)))
	return nil
}

func (p *Poller) setToken(credentials *claude.Credentials) {
	status := credentials.Status()
	p.mu.Lock()
	p.token = status
	p.mu.Unlock()
}

func (p *Poller) setPause(next *pause) {
	p.mu.Lock()
	p.pause = next
	p.mu.Unlock()
}

func (p *Poller) setOutcome(outcome string) {
	p.mu.Lock()
	p.lastOutcome = outcome
	p.mu.Unlock()
}

func (p *Poller) skip(message string) {
	p.setOutcome(outcomeSkipped)
	p.logf("건너뜀: %s", message)
}

func (p *Poller) record(email string, fetchedAt time.Time, record store.Record, outcome, message string, summary *store.Summary) {
	p.setOutcome(outcome)
	p.logf("%s", message)
	if err := p.archive.Append(email, fetchedAt, record, summary); err != nil {
		p.logf("아카이브 저장 실패: %v", err)
	}
}

func (p *Poller) logf(format string, args ...any) {
	util.Logf("[%s] %s", p.logName(), fmt.Sprintf(format, args...))
}

func (p *Poller) logName() string {
	if alias, ok := p.aliases.Lookup(p.email); ok {
		return alias
	}
	if p.email == "" {
		return p.configDir.Dir
	}
	return p.email
}

// 아카이브 한 줄이 지나치게 길어지지 않도록, 오류 응답 본문은 앞부분만 남긴다.
func clipBody(response *claude.Response) json.RawMessage {
	text := string(response.Raw)
	if response.Raw == nil {
		text, _ = response.Body.(string)
	}
	clipped, cut := truncateRunes(text, 4000)
	if !cut && response.Raw != nil {
		return response.Raw
	}
	if cut {
		clipped += "…"
	}
	encoded, _ := util.MarshalJSON(clipped)
	return encoded
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func minutes(d time.Duration) string {
	return util.FormatNumber(d.Minutes()) + "분"
}

func truncateRunes(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return text, false
	}
	return string([]rune(text)[:limit]), true
}

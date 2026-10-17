const DEFAULT_RANGE = '12h';
const SEVERITY = {
    normal: { label: '정상', color: 'var(--text-1)', icon: '●' },
    warning: { label: '주의', color: 'var(--warning)', icon: '▲' },
    serious: { label: '경고', color: 'var(--serious)', icon: '▲' },
    critical: { label: '위험', color: 'var(--critical)', icon: '■' },
};
const OUTCOME_LABELS = { ok: '성공', skipped: '건너뜀', rate_limited: 'rate limit', auth_rejected: '인증 거부', error: '실패' };
const PAUSE_LABELS = { rate_limited: 'rate limit', auth_rejected: '인증 거부' };

// F5로 새로고침해도 고른 계정과 기간이 남도록 주소의 ?account=…&range=…에서 시작한다.
const initial = new URLSearchParams(location.search);
const view = {
    range: initial.get('range') ?? DEFAULT_RANGE,
    account: Number.parseInt(initial.get('account'), 10) || 0,
    accountsKey: null,
    payload: null,
    chart: null,
};

const $ = (id) => document.getElementById(id);
const savedPassword = localStorage.getItem(AUTH_KEY);

// 주소의 ?account=…&range=…를 넘겨, 로그인한 뒤 보던 계정과 기간으로 돌아오게 한다.
function goToLogin() {
    location.replace(`/login${location.search}`);
}

// 비밀번호가 틀리면 로그인 창으로 넘어가고, 돌려준 약속은 끝나지 않는다. 넘어가는 동안 호출한 쪽이 오류를 띄우거나 화면을 바꾸지 않는다.
async function api(path, options = {}) {
    const res = await fetch(path, { ...options, headers: authHeaders(savedPassword), cache: 'no-store' });
    if (res.status === 401) {
        goToLogin();
        return new Promise(() => {});
    }
    return res;
}

let revealed = false;

// 로그인이 확인된 뒤에야 화면을 보이고 Clawd 등장 애니메이션을 튼다. 숨긴 채 틀면 아무도 보지 못하고 끝난다.
function reveal() {
    if (revealed) return;
    revealed = true;
    document.body.hidden = false;
    playClawdEntrance();
}
const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const seriesColor = (slot) => `var(--s${slot})`;

function el(tag, props = {}, ...children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
        if (value == null) continue;
        if (key === 'class') node.className = value;
        else if (key === 'style') for (const [prop, v] of Object.entries(value)) node.style.setProperty(prop, v);
        else if (key.startsWith('data-')) node.setAttribute(key, value);
        else node[key] = value;
    }
    for (const child of children) if (child != null && child !== false) node.append(child);
    return node;
}

function escapeHtml(text) {
    return String(text).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}

const timeFmt = new Intl.DateTimeFormat('ko-KR', { hour: '2-digit', minute: '2-digit', hour12: false });
const dateTimeFmt = new Intl.DateTimeFormat('ko-KR', { month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
const fullFmt = new Intl.DateTimeFormat('ko-KR', { month: 'long', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });

function pct(value) {
    return value == null ? '–' : `${Math.round(value * 10) / 10}%`;
}

// 1분이 안 되면 null이다.
function duration(deltaMs) {
    const minutes = Math.round(Math.abs(deltaMs) / 60000);
    if (minutes < 1) return null;
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);
    return days >= 1 ? `${days}일 ${hours % 24}시간` : hours >= 1 ? `${hours}시간 ${minutes % 60}분` : `${minutes}분`;
}

function relative(deltaMs) {
    const text = duration(deltaMs);
    if (text === null) return deltaMs >= 0 ? '곧' : '방금';
    return deltaMs >= 0 ? `${text} 후` : `${text} 전`;
}

function intervalLabel(ms) {
    if (ms >= 3600000 && ms % 3600000 === 0) return `${ms / 3600000}시간`;
    return ms >= 60000 ? `${Math.round(ms / 60000)}분` : `${Math.round(ms / 1000)}초`;
}

function shortDateTime(t) {
    const d = new Date(t);
    const two = (n) => String(n).padStart(2, '0');
    return `${two(d.getMonth() + 1)}/${two(d.getDate())} ${two(d.getHours())}:${two(d.getMinutes())}`;
}

function planLabel(token) {
    if (!token) return null;
    const tier = /max_(\d+x)/i.exec(token.rateLimitTier || '');
    if (tier) return `Max ${tier[1]}`;
    const type = token.subscriptionType;
    return type ? type.charAt(0).toUpperCase() + type.slice(1) : null;
}

function severityOf(reading) {
    const key = (reading.severity || '').toLowerCase();
    if (SEVERITY[key]) return SEVERITY[key];
    if (key) return { label: reading.severity, color: 'var(--warning)', icon: '▲' };
    return reading.percent >= 90 ? SEVERITY.critical : reading.percent >= 75 ? SEVERITY.warning : SEVERITY.normal;
}

function slotOf(key) {
    return view.payload?.catalog.find((series) => series.key === key)?.slot ?? null;
}

const IDLE_NOTES = {
    session: '첫 메시지를 보내면 5시간 창이 시작됩니다',
    weekly_all: '첫 메시지를 보내면 주간 창이 시작됩니다',
    weekly_scoped: '첫 메시지를 보내면 주간 창이 시작됩니다',
};

// 마지막 수집 뒤 초기화 시각이 지났다면 그 창은 끝났고, 다음 메시지 전까지 사용량은 0이다.
function currentState(reading) {
    const resets = reading.resetsAt ? Date.parse(reading.resetsAt) : NaN;
    const expired = !Number.isNaN(resets) && resets <= Date.now();
    const idle = reading.idle || expired;
    return { idle, expired, resets, percent: idle ? 0 : reading.percent };
}

function renderClawd(payload) {
    const session = payload.latest?.readings.find((r) => r.kind === 'session');
    setClawdAsleep(session ? !currentState(session).idle && session.percent >= 100 : false);
}

// ── 렌더링 ────────────────────────────────────────────────────────────────

// 탭 제목만 보고도 고른 계정의 5시간·주간 사용률을 알 수 있게 한다.
function renderTitle(payload) {
    const percentOf = (kind) => {
        const reading = payload.latest.readings.find((r) => r.kind === kind);
        return reading ? `${Math.round(currentState(reading).percent)}%` : '–';
    };
    document.title = payload.latest.fetchedAt ? `Claude Tracker: ${percentOf('session')} (${percentOf('weekly_all')})` : 'Claude Tracker';
}

function renderHeader(payload) {
    const parts = [];
    const plan = planLabel(payload.status.token);
    if (plan) parts.push(`${plan} 플랜`);
    if (payload.latest.fetchedAt) {
        const t = Date.parse(payload.latest.fetchedAt);
        parts.push(`마지막 수집 ${timeFmt.format(t)} (${relative(t - Date.now())})`);
    } else {
        parts.push('아직 수집된 데이터가 없습니다');
    }
    $('sub').textContent = parts.join(', ');
}

// 조금이라도 썼으면 한 칸은 채운다. 반올림하면 99%가 꽉 찬 막대로 보여 한도에 닿은 것처럼 읽힌다.
function filledPct(percent, cellPct) {
    const p = Math.min(100, Math.max(0, percent));
    return p === 0 ? 0 : Math.max(cellPct, Math.floor(p / cellPct) * cellPct);
}

// 바가 좁아져도 칸이 --cell-width보다 좁아지지 않게 칸 수를 줄인다. 칸이 몇 px로 좁아지면 틈이 들쭉날쭉해 보인다.
function fitCells(meter, width) {
    const style = getComputedStyle(meter);
    const cellWidth = parseFloat(style.getPropertyValue('--cell-width'));
    const gap = parseFloat(style.getPropertyValue('--gap'));
    const count = Math.max(1, Math.min(Number(meter.dataset.maxCells), Math.floor((width + gap) / (cellWidth + gap))));
    meter.style.setProperty('--cells', String(count));
    meter.style.setProperty('--pct', `${filledPct(Number(meter.dataset.percent), 100 / count)}%`);
}

// 바는 새로고침마다 새로 만든다. 문서에서 빠진 바는 크기가 0이 되며 한 번 더 알려 오므로 그때 관찰을 푼다.
const cellsObserver = new ResizeObserver((entries) => {
    for (const entry of entries) {
        if (entry.target.isConnected) fitCells(entry.target, entry.contentRect.width);
        else cellsObserver.unobserve(entry.target);
    }
});

function cells(percent, { count = 50, color = 'var(--text-1)' }) {
    const meter = el('div', {
        class: 'cells',
        'data-max-cells': String(count), 'data-percent': String(percent),
        style: { '--cells': String(count), '--pct': `${filledPct(percent, 100 / count)}%`, '--fill': color },
    });
    cellsObserver.observe(meter);
    return meter;
}

function footText(reading, state) {
    if (state.expired) return '초기화됨, 다음 메시지부터 새 창이 시작됩니다';
    if (state.idle) return IDLE_NOTES[reading.kind] ?? '첫 메시지를 보내면 시작됩니다';
    if (Number.isNaN(state.resets)) return '';
    const at = `(${shortDateTime(state.resets)})`;
    if (reading.kind === 'session') return `${relative(state.resets - Date.now())} 초기화 ${at}`;
    // 주간 칸은 좁은 사용률 칸에서 이름 옆에 놓이므로, 남은 기간과 시각만 짧게 적는다.
    return `${duration(state.resets - Date.now()) ?? '곧'} ${at}`;
}

function readingRow(reading, className) {
    const state = currentState(reading);
    const severity = state.idle ? null : severityOf(reading);
    const slot = reading.field ? slotOf(reading.field) : null;
    return el('article', { class: className },
        el('div', { class: 'r-label' },
            slot ? el('span', { class: 'line-key', style: { background: seriesColor(slot) } }) : null,
            reading.label,
            severity && severity !== SEVERITY.normal ? el('span', { class: 'sev', style: { color: severity.color } }, severity.label) : null,
        ),
        el('div', { class: 'r-value' }, String(Math.round(state.percent)), el('span', { class: 'unit' }, '%')),
        cells(state.percent, { color: severity?.color }),
        el('div', { class: 'r-foot' }, footText(reading, state)),
    );
}

function renderMeters(payload) {
    const root = $('meters');
    root.replaceChildren();
    const readings = payload.latest?.readings ?? [];
    if (readings.length === 0) {
        root.append(el('p', { class: 'placeholder' }, '수집이 한 번 성공하면 현재 사용률이 여기에 표시됩니다.'));
        return;
    }
    const primary = readings.find((r) => r.kind === 'session') ?? readings[0];
    const others = readings.filter((r) => r !== primary);
    root.append(readingRow(primary, 'primary'));
    if (others.length > 0) root.append(el('div', { class: 'others' }, ...others.map((r) => readingRow(r, 'reading'))));
}

function syncFilters() {
    for (const button of $('filters').querySelectorAll('button')) {
        button.classList.toggle('on', button.dataset.range === view.range);
    }
}

function renderAccounts(payload) {
    const select = $('account');
    select.hidden = false;
    // 목록을 펼쳐 둔 사이 자동 새로고침이 항목을 갈아 끼우지 않도록, 계정 목록이 바뀔 때만 다시 만든다.
    const key = JSON.stringify(payload.accounts);
    if (key !== view.accountsKey) {
        view.accountsKey = key;
        select.replaceChildren(...payload.accounts.map((account, index) => el('option', { value: String(index) }, account.alias ?? account.email ?? `계정 ${index + 1}`)));
    }
    select.value = String(view.account);
}

function seriesData(payload, columnIndex) {
    return payload.points.map((row) => [row[0], row[columnIndex]]);
}

function tooltipHtml(params) {
    const list = Array.isArray(params) ? params : [params];
    if (list.length === 0) return '';
    const t = list[0].value?.[0] ?? list[0].axisValue;
    const rows = list
        .filter((p) => p.value && p.value[1] != null)
        .map((p) => `<div class="tt-row"><span class="line-key" style="background:${p.color}"></span><strong>${escapeHtml(pct(p.value[1]))}</strong><span class="tt-name">${escapeHtml(p.seriesName)}</span></div>`);
    return `<div class="tt-time">${escapeHtml(fullFmt.format(t))}</div>${rows.join('') || '<div class="tt-name">데이터 없음</div>'}`;
}

function renderChart(payload) {
    const container = $('chart');
    if (!view.chart) {
        view.chart = echarts.init(container, null, { renderer: 'svg' });
        new ResizeObserver(() => view.chart.resize()).observe(container);
    }
    const hasData = payload.points.length > 0 && payload.catalog.length > 0;
    $('chart-empty').hidden = hasData;

    const font = css('--font');
    const surface = css('--surface');
    const directLabels = payload.catalog.length <= 4;
    const series = payload.catalog.map((s, i) => {
        const color = css(`--s${s.slot}`);
        return {
            type: 'line',
            name: s.label,
            data: seriesData(payload, i + 1),
            showSymbol: false,
            symbol: 'circle',
            symbolSize: 8,
            connectNulls: true,
            // 서버가 범위 직전 기록을 하나 더 보내므로, 축 밖으로 나간 선을 잘라야 왼쪽 끝이 실제 기울기로 축 시작에 닿는다.
            clip: true,
            // 주간 선이 5시간 선과 겹쳐도 묻히지 않게 굵게 그린다.
            lineStyle: { width: s.key === 'sevenDay' ? 3 : 2, color, cap: 'round', join: 'round' },
            itemStyle: { color, borderColor: surface, borderWidth: 2 },
            emphasis: { disabled: true },
            endLabel: {
                show: directLabels,
                formatter: (p) => pct(p.value?.[1]),
                color: css('--text-2'),
                fontFamily: font,
                fontSize: 12,
            },
            // 끝 라벨이 겹치면 밀어내지 않고 숨긴다. 값은 툴팁과 위쪽 사용률 칸에 그대로 남는다.
            labelLayout: { hideOverlap: true },
        };
    });

    view.chart.setOption({
        backgroundColor: 'transparent',
        animation: false,
        textStyle: { fontFamily: font },
        grid: { left: 48, right: directLabels ? 56 : 20, top: 16, bottom: 32 },
        xAxis: {
            type: 'time',
            min: payload.range.from,
            max: payload.range.to,
            axisLine: { lineStyle: { color: css('--axis'), width: 1 } },
            axisTick: { show: false },
            splitLine: { show: false },
            axisLabel: {
                color: css('--muted'),
                hideOverlap: true,
                formatter: { year: '{yyyy}', month: '{M}월', day: '{M}/{d}', hour: '{HH}:{mm}', minute: '{HH}:{mm}', second: '{HH}:{mm}:{ss}', millisecond: '{HH}:{mm}:{ss}', none: '{M}/{d} {HH}:{mm}' },
            },
        },
        yAxis: {
            type: 'value',
            min: 0,
            max: (extent) => Math.max(100, Math.ceil(extent.max / 25) * 25),
            interval: 25,
            axisLine: { show: false },
            axisTick: { show: false },
            axisLabel: { color: css('--muted'), formatter: '{value}%' },
            splitLine: { lineStyle: { color: css('--grid'), width: 1, type: 'solid' } },
        },
        tooltip: {
            trigger: 'axis',
            confine: true,
            // 나타날 때의 페이드와 위치 이동 애니메이션이 도는 동안 브라우저가 글자를 흐리게 그렸다가 끝난 뒤에야 다시 선명하게 그린다.
            transitionDuration: 0,
            axisPointer: { type: 'line', lineStyle: { color: css('--axis'), width: 1, type: 'solid' }, label: { show: false } },
            backgroundColor: surface,
            borderColor: css('--border'),
            borderWidth: 1,
            padding: [8, 12],
            textStyle: { color: css('--text-1'), fontFamily: font, fontSize: 12 },
            extraCssText: 'border-radius: 4px; box-shadow: none;',
            formatter: tooltipHtml,
        },
        series,
    }, { notMerge: true });
}

// 주간 한도를 7일에 고르게 쓰면 하루 약 14.3%다. 그보다 많이 쓴 날(15% 이상)부터 짙은 두 단계로 칠한다.
const ACTIVITY_STEPS = [5, 10, 15, 20];
const DAY_MS = 86_400_000;
const WEEKDAY_NAMES = ['일', '월', '화', '수', '목', '금', '토'];

function activityLevel(increase) {
    if (!(increase > 0)) return 0;
    return 1 + ACTIVITY_STEPS.filter((step) => increase >= step).length;
}

// 서버가 보낸 날짜 문자열을 UTC 자정으로 다뤄, 브라우저 시간대와 상관없이 날짜 계산이 어긋나지 않게 한다.
function utcDay(text) {
    const [y, m, d] = text.split('-').map(Number);
    return Date.UTC(y, m - 1, d);
}

// 달 이름은 그 달 1일이 든 주 위에 단다. 첫 달은 칸이 몇 주 안 되면 다음 달 이름과 겹치므로 뺀다.
function monthLabels(start, gridStart, end) {
    const labels = [{ week: 0, month: new Date(start).getUTCMonth() }];
    for (let t = start; t <= end; t += DAY_MS) {
        const date = new Date(t);
        if (date.getUTCDate() === 1 && t !== start) labels.push({ week: Math.floor((t - gridStart) / DAY_MS / 7), month: date.getUTCMonth() });
    }
    if (labels.length > 1 && labels[1].week - labels[0].week < 3) labels.shift();
    return labels;
}

function renderActivity(payload) {
    const root = $('activity');
    const { from, to, days } = payload.activity;
    const start = utcDay(from);
    const end = utcDay(to);
    // GitHub처럼 한 줄이 한 요일(일요일부터), 한 칸 열이 한 주다.
    const gridStart = start - new Date(start).getUTCDay() * DAY_MS;
    root.style.setProperty('--weeks', String(Math.floor((end - gridStart) / DAY_MS / 7) + 1));

    const items = monthLabels(start, gridStart, end).map(({ week, month }) =>
        el('span', { class: 'activity-month', style: { 'grid-column': String(week + 2) } }, `${month + 1}월`));
    for (const weekday of [1, 3, 5]) {
        items.push(el('span', { class: 'activity-weekday', style: { 'grid-row': String(weekday + 2) } }, WEEKDAY_NAMES[weekday]));
    }
    for (let t = start; t <= end; t += DAY_MS) {
        const date = new Date(t);
        const index = (t - gridStart) / DAY_MS;
        const increase = days[date.toISOString().slice(0, 10)];
        const label = `${date.getUTCMonth() + 1}월 ${date.getUTCDate()}일(${WEEKDAY_NAMES[date.getUTCDay()]})`;
        items.push(el('span', {
            class: 'activity-cell',
            'data-level': String(activityLevel(increase)),
            'data-tip': `${label} ${increase == null ? '기록 없음' : `+${pct(increase)}`}`,
            style: { 'grid-row': String(index % 7 + 2), 'grid-column': String(Math.floor(index / 7) + 2) },
        }));
    }
    // 자동 새로고침이 칸을 새로 만들면 떠 있던 툴팁이 가리키는 칸이 사라진다.
    hideTip();
    root.replaceChildren(...items);
}

// 칸마다 툴팁을 만들지 않고 하나를 옮겨 쓴다.
function showTip(target) {
    const tip = $('tip');
    tip.textContent = target.dataset.tip;
    tip.hidden = false;
    const rect = target.getBoundingClientRect();
    const center = rect.left + rect.width / 2;
    // 화면 끝의 칸이면 툴팁을 안쪽으로 밀고, 꼬리만 칸을 가리키게 둔다.
    const half = tip.offsetWidth / 2;
    const x = Math.min(Math.max(center, half + 4), document.documentElement.clientWidth - half - 4);
    tip.style.left = `${x}px`;
    tip.style.top = `${rect.top}px`;
    tip.style.setProperty('--arrow', `${center - x}px`);
}

function hideTip() {
    $('tip').hidden = true;
}

const activityPanel = document.querySelector('.activity');
activityPanel.addEventListener('mouseover', (event) => {
    const target = event.target.closest('[data-tip]');
    if (target) showTip(target);
    else hideTip();
});
activityPanel.addEventListener('mouseleave', hideTip);
// 툴팁은 화면에 고정된 위치라, 페이지를 스크롤하면 칸에서 떨어진다.
window.addEventListener('scroll', hideTip, { passive: true });

function renderBreakdown(payload) {
    const root = $('breakdown');
    root.replaceChildren();
    const breakdown = payload.latest?.breakdown;
    if (!breakdown || breakdown.rows.length === 0) {
        root.append(el('p', { class: 'placeholder' }, '응답에 사용처 정보가 없습니다.'));
        return;
    }
    if (breakdown.asOf) root.append(el('p', { class: 'hint' }, `${dateTimeFmt.format(Date.parse(breakdown.asOf))} 기준, 이번 주 사용량 중 비율`));
    const bars = el('div', { class: 'bars' });
    for (const row of breakdown.rows) {
        bars.append(
            el('span', {}, row.label),
            cells(row.percent, { count: 25 }),
            el('span', { class: 'bar-val' }, pct(row.percent)),
        );
    }
    root.append(bars);
}

function scheduleText(status) {
    const { schedule } = status;
    if (status.limitReached) return `${intervalLabel(status.intervalMs)}, 한도 도달`;
    const current = intervalLabel(schedule.intervalMs);
    if (schedule.unchangedPollsToSlowDown === null) return `${current}, 변화가 생기면 ${intervalLabel(schedule.baseIntervalMs)}으로`;
    return `${current}, 연속 ${schedule.unchangedPolls}/${schedule.unchangedPollsToSlowDown}회 변화 없음`;
}

function renderStatus(payload) {
    const s = payload.status;
    const at = (iso) => (iso ? `${fullFmt.format(Date.parse(iso))} (${relative(Date.parse(iso) - Date.now())})` : '—');
    $('st-success').textContent = s.lastSuccessAt ? at(s.lastSuccessAt) : '이번 실행에서는 아직 없음';
    $('st-attempt').textContent = s.lastAttemptAt ? `${at(s.lastAttemptAt)}, ${OUTCOME_LABELS[s.lastOutcome] ?? s.lastOutcome ?? ''}` : '—';
    $('st-next').textContent = at(s.nextPollAt);
    $('st-paused-row').hidden = !s.pausedUntil;
    $('st-paused').textContent = s.pausedUntil ? `${at(s.pausedUntil)}까지 (${PAUSE_LABELS[s.pauseReason] ?? s.pauseReason})` : '';
    $('st-interval').textContent = scheduleText(s);
    $('st-change').textContent = s.schedule.lastChangeAt ? at(s.schedule.lastChangeAt) : '—';
    $('st-token').textContent = s.token?.expiresAt ? at(new Date(s.token.expiresAt).toISOString()) : '—';
    $('st-refresh-token').textContent = s.token?.refreshTokenExpiresAt ? at(new Date(s.token.refreshTokenExpiresAt).toISOString()) : '—';
    const missingScopes = s.token?.missingScopes ?? [];
    $('st-scopes-row').hidden = missingScopes.length === 0;
    $('st-scopes').textContent = missingScopes.join(', ');
}

function render() {
    const payload = view.payload;
    if (!payload) return;
    renderAccounts(payload);
    renderTitle(payload);
    renderHeader(payload);
    renderClawd(payload);
    renderMeters(payload);
    renderChart(payload);
    renderActivity(payload);
    renderBreakdown(payload);
    renderStatus(payload);
}

// ── 데이터·상호작용 ───────────────────────────────────────────────────────

function showError(message) {
    $('error').hidden = !message;
    $('error-text').textContent = message ?? '';
}

let lastRequest = 0;

async function load({ keepFrame = false } = {}) {
    const request = ++lastRequest;
    const main = $('content');
    if (keepFrame) main.classList.add('is-loading');
    try {
        const res = await api(`/api/dashboard?range=${encodeURIComponent(view.range)}&account=${view.account}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const payload = await res.json();
        // 계정이나 기간을 바꾸는 사이 늦게 도착한 이전 요청의 응답이 새 화면과 고른 계정을 되돌리지 않게 버린다.
        if (request !== lastRequest) return;
        // 허용 IP가 아니면 비밀번호가 비어 있어 받아 준 것이므로, 로그인 창에서 빈 비밀번호로 로그인하게 한다.
        if (savedPassword === null && !payload.ipAllowed) {
            goToLogin();
            return;
        }
        // 허용 IP에서는 로그아웃해도 다시 로그인 없이 열리므로 버튼을 두지 않는다.
        $('logout').hidden = payload.ipAllowed;
        view.payload = payload;
        // 주소에 없는 계정·기간이 적혀 있거나 설정이 바뀌어 고른 계정이 없어졌으면 서버가 기본값을 보내므로 그쪽에 맞춘다.
        view.account = payload.account;
        view.range = payload.range.key;
        syncFilters();
        saveSelection();
        showError(null);
        // 그래프는 그릴 때의 크기를 재므로, 숨긴 화면에 그리지 않게 먼저 보인다.
        reveal();
        render();
    } catch (error) {
        if (request === lastRequest) {
            reveal();
            showError(`대시보드 데이터를 불러오지 못했습니다: ${error.message}`);
        }
    } finally {
        if (request === lastRequest) main.classList.remove('is-loading');
    }
}

// 고를 때마다 뒤로 가기 기록이 쌓이지 않게 지금 기록의 주소만 바꾼다.
function saveSelection() {
    const url = new URL(location.href);
    url.searchParams.set('account', String(view.account));
    url.searchParams.set('range', view.range);
    history.replaceState(null, '', url);
}

function selectRange(key) {
    view.range = key;
    syncFilters();
    load({ keepFrame: true });
}

function selectAccount(index) {
    view.account = index;
    load({ keepFrame: true });
}

for (const button of $('filters').querySelectorAll('button')) {
    button.addEventListener('click', () => selectRange(button.dataset.range));
}

$('account').addEventListener('change', (event) => selectAccount(Number(event.target.value)));

const AUTO_REFRESH_MS = 60_000;
let autoRefreshTimer = null;

function setAutoRefresh(on) {
    clearInterval(autoRefreshTimer);
    autoRefreshTimer = on ? setInterval(load, AUTO_REFRESH_MS) : null;
    const button = $('auto-refresh');
    button.classList.toggle('on', on);
    button.textContent = on ? '자동 새로고침: 켜짐' : '자동 새로고침: 꺼짐';
}

$('auto-refresh').addEventListener('click', () => {
    const on = autoRefreshTimer === null;
    setAutoRefresh(on);
    if (on) load();
});

// 수집이 끝날 즈음 한 번 더 불러와, 자동 새로고침을 꺼 두었어도 눌러서 받은 값이 화면에 나오게 한다.
const AFTER_POLL_MS = 5_000;
let afterRefreshTimer = null;

async function refreshNow() {
    const button = $('refresh-now');
    button.disabled = true;
    try {
        const res = await api(`/api/refresh?account=${view.account}`, { method: 'POST' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const status = await res.json();
        await load();
        clearTimeout(afterRefreshTimer);
        afterRefreshTimer = status.nextPollAt ? setTimeout(load, Math.max(0, Date.parse(status.nextPollAt) - Date.now()) + AFTER_POLL_MS) : null;
    } catch (error) {
        showError(`새로고침을 요청하지 못했습니다: ${error.message}`);
    } finally {
        button.disabled = false;
    }
}

$('refresh-now').addEventListener('click', refreshNow);

$('logout').addEventListener('click', () => {
    localStorage.removeItem(AUTH_KEY);
    goToLogin();
});

syncFilters();
load();
setAutoRefresh(true);

// Claude Code 2.1.288 바이너리에 있는 Clawd의 포즈·글리프 표와 등장 애니메이션 프레임을 그대로 옮겼다.
// 터미널 한 칸을 가로 2 × 세로 4 단위로 보고, 블록 문자가 칠하는 부분을 사각형으로 그린다.

const CLAWD_COLS = 9;
const CLAWD_ROWS = 3;
const CLAWD_FRAME_MS = 60;
const SVG_NS = 'http://www.w3.org/2000/svg';

const UL = [0, 0, 1, 2];
const UR = [1, 0, 1, 2];
const LL = [0, 2, 1, 2];
const LR = [1, 2, 1, 2];
const CLAWD_GLYPHS = {
    '█': [[0, 0, 2, 4]],
    '▀': [[0, 0, 2, 2]],
    '▄': [[0, 2, 2, 2]],
    '▌': [[0, 0, 1, 4]],
    '▐': [[1, 0, 1, 4]],
    '▘': [UL],
    '▝': [UR],
    '▗': [LR],
    '▛': [UL, UR, LL],
    '▜': [UL, UR, LR],
    '▟': [UR, LL, LR],
    '▂': [[0, 3, 2, 1]],
    '▁': [[0, 3.5, 2, 0.5]],
};

const CLAWD_POSES = {
    default: { eyes: 'open', arms: 'down', feet: 'both' },
    'arms-up': { eyes: 'open', arms: 'up', feet: 'both' },
    'look-left': { eyes: 'left', arms: 'down', feet: 'both' },
    'look-right': { eyes: 'right', arms: 'down', feet: 'both' },
};
const CLAWD_ARMS = {
    down: { r1L: ' ▐', r1R: '', r2L: '▝▜', r2R: '█▀' },
    up: { r1L: '▗▟', r1R: '▄', r2L: ' ▜', r2R: '█▘' },
    'one-up': { r1L: ' ▐', r1R: '▄', r2L: '▝▜', r2R: '█▘' },
};
const CLAWD_EYES = {
    open: [{ glyphs: '▛███▛█' }],
    left: [{ glyphs: '▟███▟█' }],
    right: [{ glyphs: '█▟███▟' }],
    closed: [{ glyphs: '▂', lid: true }, { glyphs: '███' }, { glyphs: '▂', lid: true }, { glyphs: '█' }],
    wink: [{ glyphs: '▛███' }, { glyphs: '▂', lid: true }, { glyphs: '█' }],
};
const CLAWD_FEET = {
    both: ' ▝▝   ▝▝ ',
    left: ' ▝▝      ',
    right: '      ▝▝ ',
};
// [글리프, 검은 바탕 시작 칸, 끝 칸]
const CLAWD_FACING = {
    'right-12': [[' ▐█▜██▛█ ', 2, 8], ['▝▜██████▀', 2, 7], [' ▝▝   ▝▝ ', 0, 0]],
    'right-30': [['  █▛██▛▌ ', 2, 7], [' ▝█████▛ ', 2, 7], ['  ▘▘  ▘▘ ', 0, 0]],
    'right-55': [['  ▐█▛█▜  ', 3, 7], ['  ▐████  ', 3, 7], ['  ▝▝ ▝▝  ', 0, 0]],
    'right-75': [['   ██▛▌  ', 3, 6], ['   ███▌  ', 3, 6], ['   ▘  ▘  ', 0, 0]],
    edge: [['   ▐██   ', 4, 6], ['   ▐██   ', 4, 6], ['   ▝ ▝   ', 0, 0]],
    'back-105': [['   ███▌  ', 3, 6], ['   ███▌  ', 3, 6], ['   ▘  ▘  ', 0, 0]],
    'back-125': [['  ▐████  ', 3, 7], ['  ▐████  ', 3, 7], ['  ▝▝ ▝▝  ', 0, 0]],
    'back-150': [['  █████▌ ', 2, 7], [' ▝█████▛ ', 2, 7], ['  ▘▘  ▘▘ ', 0, 0]],
    back: [[' ▐██████ ', 2, 8], ['▝▜██████▀', 2, 7], [' ▝▝   ▝▝ ', 0, 0]],
    'left-75': [['   ▛██▌  ', 3, 6], ['   ███▌  ', 3, 6], ['   ▘  ▘  ', 0, 0]],
    'left-55': [['  ▐▜▛██  ', 3, 7], ['  ▐████  ', 3, 7], ['  ▝▝ ▝▝  ', 0, 0]],
    'left-30': [['  ▛██▛█▌ ', 2, 7], [' ▝█████▛ ', 2, 7], ['  ▘▘  ▘▘ ', 0, 0]],
    'left-12': [[' ▐▛███▜█ ', 2, 8], ['▝▜██████▀', 2, 7], [' ▝▝   ▝▝ ', 0, 0]],
};
const CLAWD_POOF = { dot: '·', wave: '~' };
const CLAWD_SHADOW = { wide: { glyphs: '▁▁▁', left: 3 }, narrow: { glyphs: '▁', left: 4 } };

const CLAWD_REST = { pose: 'default', offset: 0 };
const CLAWD_ASLEEP = { pose: { eyes: 'closed', arms: 'down', feet: 'both' }, offset: 0 };

// offset은 아래로 내려가는 줄 수(음수면 위), x는 오른쪽으로 옮기는 칸 수다. 틀 밖으로 나간 부분은 잘린다.
const CLAWD_ENTRANCES = (() => {
    const hold = (pose, count, { offset = 0, x, poof, shadow } = {}) => Array.from({ length: count }, () => ({ pose, offset, x, poof, shadow }));
    const crouch = () => [{ pose: 'default', offset: 1, poof: 'dot' }, { pose: 'default', offset: 1, poof: 'wave' }];
    const facing = (names, shadow) => names.flatMap((name) => hold({ facing: name }, 1, { shadow }));
    const pose = (eyes, arms = 'down', feet = 'both') => ({ eyes, arms, feet });
    const blink = [...hold(pose('closed'), 1), CLAWD_REST];
    const jump = [...crouch(), ...hold('arms-up', 3), ...hold('default', 1), ...crouch(), ...hold('arms-up', 3), ...hold('default', 1)];
    return {
        jump,
        look: [...hold('look-right', 5), ...hold('look-left', 5), ...hold('default', 1)],
        celebrate: [...jump, ...hold('default', 3, { offset: 1 })],
        skip: [
            ...hold('default', 1, { offset: 1, x: -CLAWD_COLS }), ...hold('arms-up', 2, { x: -6 }), ...hold('default', 1, { x: -6 }),
            ...hold('default', 1, { offset: 1, x: -6 }), ...hold('arms-up', 2, { x: -3 }), ...hold('default', 1, { x: -3 }),
            ...hold('default', 1, { offset: 1, x: -3 }), ...hold('arms-up', 2), ...crouch(), ...hold('default', 1),
        ],
        spin: [...hold('look-left', 2), ...hold('look-right', 2), ...hold('look-left', 2), ...hold('arms-up', 3), ...hold('default', 1)],
        peekaboo: [
            ...hold('default', 1, { offset: CLAWD_ROWS }), ...hold('default', 3, { offset: 2 }), ...hold('look-right', 3, { offset: 2 }),
            ...hold('look-left', 3, { offset: 2 }), ...hold('default', 2, { offset: 2 }), ...hold('default', 1, { offset: 1 }),
            ...hold('arms-up', 4), ...hold('default', 2), ...blink,
        ],
        drop: [
            ...hold('arms-up', 1, { offset: -CLAWD_ROWS }), ...hold('arms-up', 2, { offset: -2 }), ...hold('arms-up', 2, { offset: -1 }),
            ...hold('arms-up', 1), ...crouch(), ...hold('default', 3), ...blink,
        ],
        waddle: [
            ...hold('look-right', 1, { x: -CLAWD_COLS }),
            ...[-6, -5, -4, -3, -2, -1].flatMap((x) => hold(pose('right', 'down', x % 2 === 0 ? 'left' : 'right'), 2, { x })),
            ...hold('look-right', 2), ...hold('default', 2), ...blink,
        ],
        peek: [
            ...hold('look-right', 1, { x: -CLAWD_COLS }), ...hold('look-right', 5, { x: -6 }), ...hold('look-right', 3, { x: -CLAWD_COLS }),
            ...hold('look-right', 4, { x: -5 }), ...hold('arms-up', 2, { x: -3 }), ...hold('arms-up', 2), ...crouch(), CLAWD_REST,
        ],
        wink: [...hold(pose('wink'), 5), CLAWD_REST],
        boop: [
            ...hold(pose('closed'), 1, { offset: 1, poof: 'dot' }), ...hold(pose('closed'), 2, { offset: 1, poof: 'wave' }),
            ...hold('look-left', 4), ...hold('default', 2), ...blink,
        ],
        tap: [
            ...hold(pose('open', 'down', 'left'), 2), ...hold(pose('open', 'down', 'right'), 2),
            ...hold(pose('open', 'down', 'left'), 2), ...hold(pose('open', 'down', 'right'), 2),
            ...hold(pose('open', 'down', 'left'), 1), ...hold(pose('open', 'down', 'right'), 1),
            ...hold('arms-up', 3), CLAWD_REST,
        ],
        sneeze: [
            ...hold(pose('closed', 'up'), 4), ...hold(pose('closed'), 2, { offset: 1, poof: 'wave' }), ...hold(pose('closed'), 2),
            ...hold('default', 2), ...blink,
        ],
        turn: [
            ...facing(['right-12', 'right-30', 'right-55', 'right-75', 'edge']), ...facing(['back-105', 'back-125', 'back-150', 'back', 'back']),
            ...facing(['back-150', 'back-125', 'back-105', 'edge']), ...facing(['left-75', 'left-55', 'left-30', 'left-12']), CLAWD_REST,
        ],
        'coin-hop': [
            ...hold('default', 2, { offset: 1 }), ...hold('arms-up', 1, { shadow: 'wide' }),
            ...facing(['right-55', 'edge', 'back-125', 'back'], 'narrow'), ...facing(['back-125', 'edge', 'left-55'], 'narrow'),
            ...hold('arms-up', 1, { shadow: 'wide' }), ...crouch(), CLAWD_REST,
        ],
    };
})();

const clawdState = { playing: false, asleep: false };

function svgEl(tag, attrs = {}) {
    const node = document.createElementNS(SVG_NS, tag);
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value));
    return node;
}

// on이 'eyes'면 칸 바탕을 검게 칠해 비어 있는 사분면이 눈이 되고, 'lid'면 색을 뒤집어 몸 색 바탕에 글리프를 검게 칠한다.
function drawGlyphs(target, glyphs, col, row, { on, fill = 'body' } = {}) {
    [...glyphs].forEach((glyph, i) => {
        const x = (col + i) * 2;
        const y = row * 4;
        if (on) target.append(svgEl('rect', { x, y, width: 2, height: 4, class: on === 'lid' ? 'body' : 'eye' }));
        for (const [dx, dy, width, height] of CLAWD_GLYPHS[glyph] ?? []) {
            target.append(svgEl('rect', { x: x + dx, y: y + dy, width, height, class: on === 'lid' ? 'eye' : fill }));
        }
    });
}

function drawPose(target, pose) {
    const arms = CLAWD_ARMS[pose.arms];
    drawGlyphs(target, arms.r1L, 0, 0);
    let col = 2;
    for (const span of CLAWD_EYES[pose.eyes]) {
        drawGlyphs(target, span.glyphs, col, 0, { on: span.lid ? 'lid' : 'eyes' });
        col += span.glyphs.length;
    }
    drawGlyphs(target, arms.r1R, 8, 0);
    drawGlyphs(target, arms.r2L, 0, 1);
    drawGlyphs(target, '█████', 2, 1, { on: 'eyes' });
    drawGlyphs(target, arms.r2R, 7, 1);
    drawGlyphs(target, CLAWD_FEET[pose.feet], 0, 2);
}

function drawFacing(target, rows) {
    rows.forEach(([glyphs, from, to], row) => {
        drawGlyphs(target, glyphs.slice(0, from), 0, row);
        drawGlyphs(target, glyphs.slice(from, to), from, row, { on: 'eyes' });
        drawGlyphs(target, glyphs.slice(to), to, row);
    });
}

function drawClawd(target, frame) {
    const sprite = svgEl('g', { transform: `translate(${(frame.x ?? 0) * 2} ${frame.offset * 4})` });
    const pose = typeof frame.pose === 'string' ? CLAWD_POSES[frame.pose] : frame.pose;
    if (pose.facing) drawFacing(sprite, CLAWD_FACING[pose.facing]);
    else drawPose(sprite, pose);
    const layers = [sprite];
    // 먼지와 그림자는 몸을 따라 움직이지 않고 바닥 줄에 남는다.
    const floor = CLAWD_ROWS - 1;
    if (frame.poof && frame.offset > 0) {
        for (const col of [0, CLAWD_COLS - 1]) {
            const dust = svgEl('text', { x: col * 2 + 1, y: floor * 4 + 2, class: 'poof' });
            dust.textContent = CLAWD_POOF[frame.poof];
            layers.push(dust);
        }
    }
    if (frame.shadow) {
        const shadow = svgEl('g');
        drawGlyphs(shadow, CLAWD_SHADOW[frame.shadow].glyphs, CLAWD_SHADOW[frame.shadow].left, floor, { fill: 'shade' });
        layers.push(shadow);
    }
    target.replaceChildren(...layers);
}

function createClawdSvg(frame) {
    const svg = svgEl('svg', {
        class: 'clawd', viewBox: `0 0 ${CLAWD_COLS * 2} ${CLAWD_ROWS * 4}`, 'shape-rendering': 'crispEdges',
    });
    const sprite = svgEl('g');
    svg.append(sprite);
    drawClawd(sprite, frame);
    return { svg, sprite };
}

// 돌려주는 함수를 부르면 남은 프레임을 그리지 않고 멈춘다.
function playClawdFrames(target, frames, { onFrame, onDone } = {}) {
    let index = 0;
    let timer = null;
    const step = () => {
        if (index >= frames.length) {
            onDone?.();
            return;
        }
        onFrame?.(index);
        drawClawd(target, frames[index++]);
        timer = setTimeout(step, CLAWD_FRAME_MS);
    };
    step();
    return () => clearTimeout(timer);
}

function clawdRest() {
    return clawdState.asleep ? CLAWD_ASLEEP : CLAWD_REST;
}

// Claude Code처럼 움직임 줄이기 설정을 켠 사람에게는 등장 애니메이션을 보여 주지 않는다.
function playClawdEntrance() {
    const sprite = document.getElementById('clawd-sprite');
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
        drawClawd(sprite, clawdRest());
        return;
    }
    const names = Object.keys(CLAWD_ENTRANCES);
    const name = names[Math.floor(Math.random() * names.length)];
    document.getElementById('clawd').dataset.entrance = name;
    clawdState.playing = true;
    playClawdFrames(sprite, CLAWD_ENTRANCES[name], {
        onDone: () => {
            clawdState.playing = false;
            drawClawd(sprite, clawdRest());
        },
    });
}

function setClawdAsleep(asleep) {
    clawdState.asleep = asleep;
    document.getElementById('clawd-title').textContent = asleep ? 'Clawd, 5시간 한도를 다 써서 쉬는 중' : 'Clawd';
    if (!clawdState.playing) drawClawd(document.getElementById('clawd-sprite'), clawdRest());
}

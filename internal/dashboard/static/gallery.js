const MOTION_NOTES = {
    celebrate: 'Claude Code의 등장 목록에는 없고 프레임 표에만 들어 있습니다.',
};

function node(tag, props = {}, ...children) {
    const element = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
        if (value == null) continue;
        if (key === 'class') element.className = value;
        else element[key] = value;
    }
    for (const child of children) if (child != null) element.append(child);
    return element;
}

function poseLabel(pose) {
    if (typeof pose === 'string') return pose;
    if (pose.facing) return pose.facing;
    const parts = [
        pose.eyes === 'open' ? null : pose.eyes,
        pose.arms === 'down' ? null : `arms-${pose.arms}`,
        pose.feet === 'both' ? null : `feet-${pose.feet}`,
    ].filter(Boolean);
    return parts.length > 0 ? parts.join(' ') : 'default';
}

function signed(value) {
    return `${value > 0 ? '+' : '−'}${Math.abs(value)}`;
}

// 그리기와 같은 조건으로 고른다. 먼지는 몸이 내려가 있을 때만 보인다.
function moveChips(frame) {
    return [
        frame.offset ? `y${signed(frame.offset)}` : null,
        frame.x ? `x${signed(frame.x)}` : null,
        frame.poof && frame.offset > 0 ? `먼지 ${CLAWD_POOF[frame.poof]}` : null,
        frame.shadow ? `그림자 ${frame.shadow}` : null,
    ].filter(Boolean);
}

function frameCell(frame, index) {
    const pose = poseLabel(frame.pose);
    const chips = moveChips(frame);
    return node('li', { class: 'frame' },
        createClawdSvg(frame).svg,
        node('span', { class: 'frame-no' }, String(index + 1)),
        node('span', { class: 'frame-pose' }, pose),
        chips.length > 0 ? node('span', { class: 'frame-move' }, ...chips.map((chip) => node('span', {}, chip))) : null,
    );
}

function motionSection(name, frames) {
    const stage = createClawdSvg(CLAWD_REST);
    stage.svg.classList.add('clawd-stage');
    const cells = frames.map(frameCell);
    let stop = null;
    const play = () => {
        stop?.();
        stop = playClawdFrames(stage.sprite, frames, {
            onFrame: (index) => cells.forEach((cell, i) => cell.classList.toggle('is-current', i === index)),
            onDone: () => {
                cells.forEach((cell) => cell.classList.remove('is-current'));
                drawClawd(stage.sprite, CLAWD_REST);
                stop = null;
            },
        });
    };
    stage.svg.addEventListener('click', play);
    const seconds = ((frames.length * CLAWD_FRAME_MS) / 1000).toFixed(2);
    return node('section', { class: 'panel motion', id: name },
        node('div', { class: 'motion-head' },
            stage.svg,
            node('div', { class: 'motion-title' },
                node('h2', {}, name),
                node('p', { class: 'hint' }, `${frames.length}프레임, ${seconds}초`),
                MOTION_NOTES[name] ? node('p', { class: 'hint' }, MOTION_NOTES[name]) : null,
            ),
            node('button', { type: 'button', class: 'ghost', onclick: play }, '재생'),
        ),
        node('ol', { class: 'frames' }, ...cells),
    );
}

document.getElementById('motions').replaceChildren(
    ...Object.entries(CLAWD_ENTRANCES).map(([name, frames]) => motionSection(name, frames)),
);

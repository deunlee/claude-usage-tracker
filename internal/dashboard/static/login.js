// 로그인 페이지와 대시보드가 함께 읽는다. 같은 페이지의 스크립트끼리 맨 위 이름을 나눠 쓰므로,
// app.js의 $·showError와 겹치지 않게 그런 이름은 두지 않는다.

// 로그인할 때 base64로 인코딩한 비밀번호를 기한 없이 둔다. 값이 없으면(null) 로그인하지 않은 것이고,
// 빈 문자열은 config.json의 비밀번호를 비워 두어 빈 입력으로 로그인한 것이다.
const AUTH_KEY = 'dashboardPassword';

// 비밀번호가 비어 있으면 헤더를 보내지 않는다. 서버는 헤더 없는 요청을 빈 비밀번호로 본다.
function authHeaders(encoded) {
    return encoded ? { Authorization: `Bearer ${encoded}` } : {};
}

// 영문·숫자 밖의 글자가 든 비밀번호도 헤더에 넣을 수 있게 UTF-8 바이트를 base64로 바꾼다. 서버가 같은 방식으로 풀어 비교한다.
function encodePassword(password) {
    return btoa(String.fromCharCode(...new TextEncoder().encode(password)));
}

function showLoginError(message) {
    const error = document.getElementById('login-error');
    error.hidden = !message;
    error.textContent = message ?? '';
}

async function login(event) {
    event.preventDefault();
    const button = document.getElementById('login-button');
    const input = document.getElementById('password');
    button.disabled = true;
    const encoded = encodePassword(input.value);
    try {
        const res = await fetch('/api/login', { method: 'POST', headers: authHeaders(encoded), cache: 'no-store' });
        if (res.status === 401) {
            showLoginError('비밀번호가 맞지 않습니다.');
            input.select();
            return;
        }
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        localStorage.setItem(AUTH_KEY, encoded);
        // 대시보드가 로그인으로 보낼 때 붙인 ?account=…&range=…를 되돌려 주어 보던 계정과 기간으로 돌아간다.
        location.replace(`/${location.search}`);
    } catch (error) {
        showLoginError(`로그인하지 못했습니다: ${error.message}`);
    } finally {
        button.disabled = false;
    }
}

document.getElementById('login-form')?.addEventListener('submit', login);

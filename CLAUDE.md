@README.md

## 고칠 때 지킬 것

- **요청 형태를 임의로 바꾸지 않는다.** `internal/claude/client.go`의 헤더는 이름·값은 물론 **순서까지** Claude Code 바이너리를 그대로 옮긴 것이다. 타임아웃(5초)과 429 대기(기본 5분, 최대 1시간)도 같다. 고쳐야 하면 설치된 Claude Code 번들에서 `/api/oauth/usage` 호출 부분을 먼저 확인한다.
- **날짜와 시각은 `util.Local(t)`, `util.Location()`으로 적는다.** `t.Local()`이나 `time.Local`을 직접 쓰면 설정한 시간대를 무시하고 머신 시간대로 돌아, UTC로 도는 서버에서 날짜가 어긋난다.
- **외부 패키지를 더하지 않는다.** brotli 응답을 푸는 `github.com/andybalholm/brotli` 하나뿐이다.
- **토큰을 밖으로 내보내지 않는다.** `Credentials`는 토큰을 내보내지 않는 필드에 두고 `String`/`GoString`으로 로그 출력도 막아 뒀다. 새 필드를 더할 때 이 성질을 깨지 않는다.
- **아카이브 JSON의 모양을 유지한다.** TS로 만들던 시절의 파일을 그대로 읽어야 해서 `util.ISOString`과 `util.MarshalJSON`이 JavaScript의 `Date.toISOString`, `JSON.stringify`와 같은 바이트를 쓴다.
- 코드를 고쳤으면 `scripts/build.sh`로 `bin/`의 실행 파일을 다시 만든다.

## 대시보드 인증 동작

- 로그인하면 비밀번호를 base64로 인코딩해 로컬 스토리지에 저장하고, 그 뒤 API 요청마다 `Authorization: Bearer <base64>`로 보낸다. 로그아웃하면 지운다.
- `/api/`로 시작하는 요청만 비밀번호를 검사하고 틀리면 401을 돌려준다. 수집한 값이 없는 페이지와 스크립트는 그대로 보낸다. 대시보드는 401을 받으면 `/login`으로 넘어가고, 그 전까지 화면을 숨긴다.
- `ipAllowList`에 드는 IP에서 온 `/api/` 요청은 비밀번호를 검사하지 않는다(`/api/login`만 예외). 그 IP에서는 로그아웃 버튼도 숨긴다.
- 클라이언트 IP는 TCP 연결 주소다. 연결 주소가 `trustedProxies`에 들 때만 `X-Forwarded-For`를 오른쪽부터 읽어 `trustedProxies`에 없는 첫 주소를 클라이언트로 본다.

## 버전

이 프로그램의 버전은 흉내 내는 Claude Code의 버전, 곧 `internal/claude/client.go`의 `CodeVersion`을 그대로 쓴다. 따로 번호를 매기면 어느 Claude Code를 흉내 내는 빌드인지 알 수 없다. `CodeVersion`을 올리면 릴리스 태그도 같은 번호(`v2.1.302`)로 단다.

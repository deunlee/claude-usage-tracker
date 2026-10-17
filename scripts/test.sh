#!/usr/bin/env bash
# test/<패키지>/*_test.go는 비공개 이름을 쓰려고 테스트하는 패키지와 같은 패키지로 적혀 있어서,
# internal/<패키지>/ 안에 있는 것처럼 컴파일해야 한다. -overlay를 쓰면 internal/에 복사하지 않고 그렇게 된다.
set -euo pipefail

# 실행할 때마다 지우지 않고 덮어써서 임시 폴더에 쌓이지 않는다.
readonly OVERLAY_FILE="${TMPDIR:-${TEMP:-/tmp}}/claude-usage-tracker-test-overlay.json"

# Windows용 go는 Git Bash의 /c/... 경로를 읽지 못한다. 그래서 대응표 안의 경로는 저장소 루트 기준
# 상대 경로로 적고, go가 상대 경로를 루트에서 풀도록 이 스크립트는 루트에서 go를 실행한다.
write_overlay_file() {
    local separator='' file
    {
        printf '{"Replace":{'
        # find가 실패하면 pipefail로 스크립트가 멈춘다. < <(find ...)로 넣으면 그 실패가 묻힌다.
        find test -name '*_test.go' | sort | while IFS= read -r file; do
            printf '%s"internal/%s":"%s"' "$separator" "${file#test/}" "$file"
            # JSON은 마지막 항목 뒤의 쉼표를 받지 않아, 쉼표를 두 번째 항목부터 앞에 붙인다.
            separator=','
        done
        printf '}}\n'
    } > "$1"
}

# Linux·macOS에는 cygpath가 없고 경로를 바꿀 필요도 없다.
path_for_go() {
    if command -v cygpath > /dev/null; then
        cygpath -w "$1"
    else
        printf '%s\n' "$1"
    fi
}

# 결과는 전역 배열 go_test_args에 담는다. bash 함수는 배열을 돌려줄 수 없다.
make_go_test_args() {
    local arg has_package=false
    go_test_args=()
    for arg in "$@"; do
        # test/<패키지>에는 태그로 빠지는 파일뿐이라 go test가 테스트를 찾지 못한다.
        if [[ "$arg" =~ ^(\./)?test(/|$) ]]; then
            arg="${arg/test/internal}"
        fi
        # -run 값 같은 옵션 인자를 패키지로 착각하지 않도록, 저장소 안 경로만 패키지로 친다.
        if [[ "$arg" =~ ^(\./)?internal(/|$) ]]; then
            has_package=true
        fi
        go_test_args+=("$arg")
    done
    # 패키지를 주지 않으면 go test는 지금 폴더(테스트가 없는 저장소 루트)만 돈다.
    if [[ "$has_package" == false ]]; then
        go_test_args+=(./internal/...)
    fi
}

main() {
    cd "$(dirname "$0")/.."
    write_overlay_file "$OVERLAY_FILE"
    make_go_test_args "$@"
    # 테스트 파일에 붙인 이 태그가 없으면 gopls와 go vet ./...가 test/<패키지>를 별개 패키지로 보고
    # 비공개 이름마다 undefined 오류를 낸다. 그래서 이 스크립트에서만 켠다.
    go test -tags separatetest -overlay="$(path_for_go "$OVERLAY_FILE")" "${go_test_args[@]}"
}

main "$@"

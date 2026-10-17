#!/usr/bin/env bash
set -euo pipefail

readonly NAME_PREFIX=cut
readonly OUT_DIR=bin
readonly TARGETS=(
    windows/amd64
    linux/amd64
    linux/arm64
)

output_path() {
    local goos=$1 goarch=$2 suffix=''
    if [[ "$goos" == windows ]]; then
        suffix=.exe
    fi
    printf '%s/%s-%s-%s%s\n' "$OUT_DIR" "$NAME_PREFIX" "$goos" "$goarch" "$suffix"
}

build_target() {
    local goos=$1 goarch=$2 output
    output="$(output_path "$goos" "$goarch")"
    # 외부 C 라이브러리를 쓰지 않으니 cgo를 꺼서, 다른 OS용도 이 PC에서 C 컴파일러 없이 만들고
    # Linux에서는 glibc 버전과 상관없이 도는 정적 실행 파일이 되게 한다.
    # 심볼 표·DWARF 디버그 정보(-s -w)와 go build가 기본으로 남기는 C:\Users\<이름>\... 같은
    # 로컬 소스 경로(-trimpath)는 실행에 필요 없다.
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags="-s -w" -o "$output" .
    printf 'Built %s\n' "$output"
}

main() {
    # 상대 경로인 bin/이 스크립트를 어디서 실행하든 저장소 루트 아래에 생기게 한다.
    cd "$(dirname "$0")/.."
    local target
    for target in "${TARGETS[@]}"; do
        build_target "${target%/*}" "${target#*/}"
    done
}

main "$@"

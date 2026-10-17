package claude

import "strings"

// 이메일 대신 보여 줄 계정 이름. 키는 소문자로 바꾼 이메일이다.
type AccountAliases map[string]string

// 계정 정보 파일의 이메일과 사람이 config.json에 적은 이메일은 대소문자가 다를 수 있다.
func (a AccountAliases) Lookup(email string) (string, bool) {
	alias, ok := a[strings.ToLower(email)]
	return alias, ok
}

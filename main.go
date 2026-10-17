package main

import (
	"os"
	// config.json의 timezone을 시간대 데이터베이스가 없는 OS(Windows)에서도 읽을 수 있게 실행 파일에 넣는다.
	_ "time/tzdata"

	"claudeusage/internal/app"
)

func main() {
	os.Exit(app.Run())
}

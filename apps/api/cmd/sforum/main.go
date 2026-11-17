package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	err := newRootCommand().Execute()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(exitCodeFor(err))
}

// exitCodeFor 让带外命令用退出码表达三态：0 一致/已执行、1 有缺陷或前提漂移、
// 2 用法错误。其余错误一律 1，保持既有脚本的 `!= 0` 判断不变。
func exitCodeFor(err error) int {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		if code := coded.ExitCode(); code > 0 {
			return code
		}
	}
	return 1
}

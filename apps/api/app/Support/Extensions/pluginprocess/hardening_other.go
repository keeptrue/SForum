//go:build !unix

package pluginprocess

import "os/exec"

// 非 unix 平台没有进程组与 rlimit 语义：保持签名一致、行为留空，让调用方的
// exec 路径不必按平台分叉。发布目标见 .github/workflows/release.yml（linux 与
// darwin 都是 unix），这里只是为跨平台编译留出可预期的落点。

func Apply(cmd *exec.Cmd) {}

func GroupID(cmd *exec.Cmd) int { return 0 }

func Limit(cmd *exec.Cmd) {}

func KillGroup(processGroup int) {}

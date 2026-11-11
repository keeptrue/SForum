package identity_test

import (
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// TestTrustLevelDisabledSentinelsAreIdentical 锁定 ④：两个哨兵必须是同一个错误值。
//
// 两个独立 errors.New 会让关闭态掉进 err != nil 分支 → 每请求一条 warn，
// 而 D9/F 组要求关闭态静默。这条测试必须放在外部测试包：内部测试包 import
// TrustLevels 会形成 import cycle（TrustLevels → Identity）。
func TestTrustLevelDisabledSentinelsAreIdentical(t *testing.T) {
	if !errors.Is(trustlevels.ErrDisabled, identity.ErrTrustLevelsDisabled) {
		t.Fatal("trustlevels.ErrDisabled 与 identity.ErrTrustLevelsDisabled 不是同一个值")
	}
	if trustlevels.ErrDisabled != identity.ErrTrustLevelsDisabled {
		t.Fatal("哨兵必须是同一个错误值（别名），不是同文案的两个值")
	}
}

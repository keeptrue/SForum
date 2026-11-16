package bootstrap

import (
	"fmt"

	providers "github.com/keeptrue/sforum/apps/api/app/Providers"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// finalizeAPIForumProvider 是 Forum provider 生产装配的**唯一收口点**：所有可选门
// （当前只有 P3-3 的 post_risk 人机验证）只在这一处接上，并在「没接」时显式失败。
//
// 为什么单独成函数：wireAPIDomainServices 需要真实 PG/Redis/扩展平台，单元测试组装
// 不起来；把收口点抽出来后，bootstrap 的装配用例可以直接调用它并断言只读探针，
// 于是「删掉 .WithHumanVerification(...) 这一行」会让用例变红，而不是静默地让生产
// 上 post_risk 变成零校验（这正是 P3-3 最初要修的假开关形态）。
//
// 三重防线，缺一不可：
//  1. verifier 为 nil → 返回错误（生产装配不允许「没人验证的发帖写入路径」）；
//  2. 接线后探针仍为 false → 返回错误（防止 WithHumanVerification 被改成空实现）；
//  3. 调用方把错误当错误处理（wireAPIDomainServices 直接向上抛，启动失败）。
//
// 注意 typed-nil：`(*humanverify.RuntimeService)(nil)` 装进接口后 `verifier == nil`
// 为 false，本函数会照常接线；运行期由 RuntimeService.service() 兜底成 disabled
// （零校验、不 panic），语义是「装配了但未启用」，与「装配漏了」严格区分。
func finalizeAPIForumProvider(provider *providers.ForumProvider, verifier humanverify.Verifier) (*providers.ForumProvider, error) {
	if provider == nil {
		return nil, fmt.Errorf("forum provider is required")
	}
	if verifier == nil {
		return nil, fmt.Errorf("forum post_risk human verification verifier is required")
	}
	wired := provider.WithHumanVerification(verifier)
	if !wired.HasHumanVerification() {
		return nil, fmt.Errorf("forum post_risk human verification was not wired")
	}
	return wired, nil
}

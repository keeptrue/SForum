package forumcontroller

import (
	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// humanVerificationRequest 与登录/注册侧同形：{provider, token}。
// 前端把 ALTCHA widget 解出的 payload 原样放进 token。
type humanVerificationRequest struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
}

// WithHumanVerification 注入发帖/回帖的 post_risk 人机验证器（P3-3）。
//
// nil 安全：未注入时（测试装配、以及无人机验证依赖的启动路径）发帖/回帖不校验。
// 注入的实例应当是 humanverify.RuntimeService —— 它每请求从 Options 读
// `human_verification.scenarios.post_risk`，因此 controller 里不需要再存一份开关状态，
// 运营改选项后立即生效，不存在「进程启动时快照」的假开关。
func (h *Controller) WithHumanVerification(verifier humanverify.Verifier) *Controller {
	if h != nil {
		h.humanVerification = verifier
	}
	return h
}

// requirePostRiskVerification 在写入前执行 post_risk 场景校验。
//
// 开关语义：provider 未启用（human_verification.provider != altcha）或该场景关闭时，
// verifier 直接返回 nil —— 不触达存储、不做 replay 判定、不计限流，所以「关闭」是
// 零校验而不是「拦下所有人」；运营打开开关后同一段代码才开始拒绝缺 token 的请求。
//
// 失败返回的 reason/字段与登录侧完全一致，由 apphttp.MapHumanVerificationError 统一
// 映射（前端按 `data.reason` 与 `data.fields.humanVerification` 定位提示）。
func (h *Controller) requirePostRiskVerification(c fiber.Ctx, actor identity.Actor, request humanVerificationRequest) error {
	if h == nil || h.humanVerification == nil {
		return nil
	}
	userID := actor.ID
	err := h.humanVerification.Verify(c.Context(), humanverify.VerifyRequest{
		Provider: request.Provider,
		Purpose:  humanverify.PurposePostRisk,
		Token:    request.Token,
		IP:       clientip.FromCtx(c),
		UserID:   &userID,
	})
	if err != nil {
		return apphttp.MapHumanVerificationError(err)
	}
	return nil
}

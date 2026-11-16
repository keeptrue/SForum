package http

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// MapHumanVerificationError 是人机验证领域错误到 HTTP 状态与 wire reason 的**唯一**映射点。
//
// 放在传输层（apphttp）而不是各 controller，理由与 MapOptionsError 同源：人机验证在
// 多条写入路径上生效（注册、登录强化、密码重置、邮件验证、发帖/回帖的 post_risk），
// 每个场景各写一份映射必然漂移。前端只认一组 `human_verification.*` reason 与
// `humanVerification` 字段名——一旦某条路径把「缺 token」报成 422
// `human_verification.invalid`，前端的「请完成人机验证」提示就会退化成通用校验失败，
// 而且这种漂移不会被任何单场景测试发现。
//
// 各分支的语义（决定前端下一步动作，不能合并）：
//   - 429 `rate_limit.exceeded` → 稍后重试（限流窗口 1 分钟，按场景 + IP）；
//   - 422 `human_verification.required` → 缺 token：渲染/重新渲染 ALTCHA 组件；
//   - 422 `human_verification.expired` → 挑战过期：重置组件重新取题；
//   - 422 `human_verification.replayed` → token 已消费：重置组件重新取题；
//   - 422 `human_verification.invalid` → token 无法验证（含 provider 不匹配）：重置组件。
//
// 字段名固定为 identity.FieldHumanVerification（"humanVerification"），与请求体里的
// `humanVerification` 对象同名，前端可以按字段定位到编辑器旁的提示位置。
func MapHumanVerificationError(err error) error {
	switch {
	case errors.Is(err, humanverify.ErrRateLimited):
		return NewErrorWithFields(fiber.StatusTooManyRequests, humanverify.CodeRateLimited, humanVerificationField(humanverify.CodeRateLimited))
	case errors.Is(err, humanverify.ErrRequired):
		return NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeRequired, humanVerificationField(humanverify.CodeRequired))
	case errors.Is(err, humanverify.ErrExpired):
		return NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeExpired, humanVerificationField(humanverify.CodeExpired))
	case errors.Is(err, humanverify.ErrReplayed):
		return NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeReplayed, humanVerificationField(humanverify.CodeReplayed))
	case errors.Is(err, humanverify.ErrInvalid):
		return NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeInvalid, humanVerificationField(humanverify.CodeInvalid))
	default:
		// 存储故障等非「用户能修」的失败原样上抛，由 errorHandler 记日志并按
		// 依赖故障/500 处理；把它映射成 422 会让前端让用户徒劳重试。
		return err
	}
}

func humanVerificationField(message string) map[string][]string {
	return map[string][]string{
		identity.FieldHumanVerification: {message},
	}
}

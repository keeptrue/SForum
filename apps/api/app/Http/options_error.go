package http

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// MapOptionsError 是 options 领域错误到 HTTP 状态与 wire reason 的**唯一**映射点。
//
// 放在传输层（apphttp）而不是 Models/Options：领域层不该把 fiber 拉进来；放在这里
// 之后，所有写 web_options 的端点（站点设置、功能开关、信任等级）共用同一份判定，
// 不会各自长出一份副本——`mapOptionsError` 曾经有两份，其中一份把写库失败报成 422，
// 于是 M12-a 修完 Options 那份之后真正被踩到的 TrustLevels 路径依然是坏的。
//
// 四类失败各有独立 reason，因为运营的修复动作不同：
//   - 归一化失败 → 422 `options.invalid_value`（改这个值）；
//   - isValidValueSet 拒绝 → 422 `options.invalid_value_set`（对照另一项设置）；
//   - 其余输入错误（未知键、宿主内部 revision、附件 id 非法）→ 422 `options.invalid`；
//   - 写库失败 → 500 `options.store_failure`（查库，不是改参数）。
//
// 分支顺序不可调：两个细分错误 wrap 了 ErrInvalidOption，必须排在它之前。
//
// 排查线索（从旧副本的注释里搬过来，别让运营丢了唯一线索）：状态码不再等于
// 「哪一步坏了」，但仍不是全部信息——422 `options.invalid` 是「输入类错误里的
// 兜底档」（未知键、宿主内部 revision、附件 id 非法），422 的两个细分码指向具体的
// 修复动作，500 `options.store_failure` 指向库。原始 err 只在日志里（storeFailure
// 与调用方各打一次），**排查必须看原始 err，不能只看状态码**。
//
// 分工：本函数只处理**领域层返回的错误**。请求体绑定失败（JSON 坏、字段类型不符）
// 属于传输层，由各 controller 就地回 422 `validation.invalid`，不要绕进这里——
// 那条路径上根本没有 options 错误可映射。
//
// 「只有一份」由 options_error_tripwire_test.go 用 AST 机械守住，不靠人记得。
func MapOptionsError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, options.ErrInvalidOptionValue):
		return fiber.NewError(fiber.StatusUnprocessableEntity, options.CodeInvalidValue)
	case errors.Is(err, options.ErrInvalidOptionSet):
		return fiber.NewError(fiber.StatusUnprocessableEntity, options.CodeInvalidValueSet)
	case errors.Is(err, options.ErrInvalidOption):
		return fiber.NewError(fiber.StatusUnprocessableEntity, options.CodeInvalid)
	case errors.Is(err, options.ErrStoreFailure):
		// 写库失败是服务端错误。storeFailure 已在产生处打过原始 err 的日志，
		// 这里只负责把状态码和 reason 摆正。
		return fiber.NewError(fiber.StatusInternalServerError, options.CodeStoreFailure)
	default:
		return err
	}
}

package options

import (
	"errors"
	"fmt"
	"log/slog"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// storeFailure 把「写库失败」标成可识别的服务端错误（ErrStoreFailure）。
//
// 它**不是**用户输入错误：HTTP 层必须回 500，而不是 422。把写库失败报成 422 会让
// 运营去改参数，而不是查库（M12-a 的原始症状）。
//
// 两个例外保持原语义、不升级成 500：
//   - ErrInvalidOption：store 自己判定的输入类错误（例如站点 logo 的附件 id 非法）；
//   - ErrPermissionDenied：store 侧权限判定。
//
// 原始 err 仍被包住（%w），日志在这里打一次，调用方不必各自补日志。
func storeFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidOption) || errors.Is(err, identity.ErrPermissionDenied) {
		return err
	}
	slog.Error("options store write failed", "error", err)
	return fmt.Errorf("%w: %w", ErrStoreFailure, err)
}

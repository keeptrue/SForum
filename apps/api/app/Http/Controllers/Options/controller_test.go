package optionscontroller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// TestMapOptionsErrorReasons 锁定四类失败各自的 wire reason（M12-a）。
//
// 断言用 reason 字面量而不是 options.Code* 常量：拿常量断言常量是自证式断言——
// 把常量改坏时测试仍然绿，而前端/运营拿到的正是改坏后的字符串。
func TestMapOptionsErrorReasons(t *testing.T) {
	storeCause := errors.New("connection refused")
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{
			name:       "归一化失败 → 值本身不合法",
			err:        options.ErrInvalidOptionValue,
			wantStatus: fiber.StatusUnprocessableEntity,
			wantReason: "options.invalid_value",
		},
		{
			name:       "isValidValueSet 拒绝 → 值不在允许集合内",
			err:        options.ErrInvalidOptionSet,
			wantStatus: fiber.StatusUnprocessableEntity,
			wantReason: "options.invalid_value_set",
		},
		{
			name:       "未知键等其余输入错误 → 泛化输入错误",
			err:        options.ErrInvalidOption,
			wantStatus: fiber.StatusUnprocessableEntity,
			wantReason: "options.invalid",
		},
		{
			// service 层用 fmt.Errorf("%w: %w", ErrStoreFailure, err) 产出这个形状。
			name:       "写库失败 → 服务端错误，不是 422",
			err:        fmt.Errorf("%w: %w", options.ErrStoreFailure, storeCause),
			wantStatus: fiber.StatusInternalServerError,
			wantReason: "options.store_failure",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped := apphttp.MapOptionsError(testCase.err)
			var fiberErr *fiber.Error
			if !errors.As(mapped, &fiberErr) {
				t.Fatalf("apphttp.MapOptionsError(%v) = %v，期望 *fiber.Error", testCase.err, mapped)
			}
			if fiberErr.Code != testCase.wantStatus {
				t.Fatalf("status = %d，期望 %d", fiberErr.Code, testCase.wantStatus)
			}
			if fiberErr.Message != testCase.wantReason {
				t.Fatalf("reason = %q，期望 %q", fiberErr.Message, testCase.wantReason)
			}
		})
	}
}

// TestOptionValidationErrorsStayCompatible 守住拆 reason 的兼容面：
// Models/Options 内几十处 errors.Is(err, ErrInvalidOption) 不能因为细分而漏判。
func TestOptionValidationErrorsStayCompatible(t *testing.T) {
	for _, err := range []error{options.ErrInvalidOptionValue, options.ErrInvalidOptionSet} {
		if !errors.Is(err, options.ErrInvalidOption) {
			t.Fatalf("%v 不再满足 errors.Is(err, ErrInvalidOption)", err)
		}
	}
	// 反向必须不成立：否则两个细分分支的先后顺序写错也会绿。
	if errors.Is(options.ErrInvalidOption, options.ErrInvalidOptionValue) {
		t.Fatal("泛化的 ErrInvalidOption 被误判成 ErrInvalidOptionValue")
	}
	if errors.Is(options.ErrInvalidOption, options.ErrStoreFailure) {
		t.Fatal("输入错误被误判成写库失败（会回 500）")
	}
}

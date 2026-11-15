package routes

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrCoreGuardCatalogCoverage 表示 Core 路由目录声明了一个没有实现的 evaluator。
var ErrCoreGuardCatalogCoverage = errors.New("core guard catalog references an unbound evaluator")

// unboundCatalogEvaluatorAllowlist 是「目录可以声明、但本进程故意不绑定」的显式白名单。
//
// 进入这里必须有理由，并且是**故意**的：默认路径是补实现；只有当某个 evaluator 由
// 别的进程/边界负责（例如只在 worker 侧求值）时才能落入本表。当前为空。
var unboundCatalogEvaluatorAllowlist = map[string]string{}

// ValidateCoreGuardCatalogCoverage 断言目录里每个 contextual evaluator 都在注册表里。
//
// 这是启动期闸门：目录是「已审阅的构建期策略数据」，一旦它引用了不存在的实现，
// 路由层的守卫能力就被虚报成存在。过去三条 evaluator（comment_edit /
// comment_revision_restore / topic_revision_restore）正是这样漏进来的——目录声称
// 有裁决，运行期却拿不到实现。
func ValidateCoreGuardCatalogCoverage(
	catalog []CoreRoute,
	bindings []CoreGuardEvaluatorBinding,
) error {
	bound := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		bound[binding.EvaluatorID] = struct{}{}
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(catalog))
	for _, route := range catalog {
		evaluatorID := strings.TrimSpace(route.Guard.EvaluatorID)
		if evaluatorID == "" {
			continue
		}
		if _, duplicate := seen[evaluatorID]; duplicate {
			continue
		}
		seen[evaluatorID] = struct{}{}
		if _, ok := bound[evaluatorID]; ok {
			continue
		}
		if _, allowed := unboundCatalogEvaluatorAllowlist[evaluatorID]; allowed {
			continue
		}
		missing = append(missing, evaluatorID)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"%w: %s; bind an evaluator or add the id to unboundCatalogEvaluatorAllowlist with a reason",
		ErrCoreGuardCatalogCoverage, strings.Join(missing, ", "),
	)
}

// UnboundCoreGuardCatalogEvaluatorReasons 暴露白名单内容，供测试断言「白名单只减不增」。
func UnboundCoreGuardCatalogEvaluatorReasons() map[string]string {
	reasons := make(map[string]string, len(unboundCatalogEvaluatorAllowlist))
	for id, reason := range unboundCatalogEvaluatorAllowlist {
		reasons[id] = reason
	}
	return reasons
}

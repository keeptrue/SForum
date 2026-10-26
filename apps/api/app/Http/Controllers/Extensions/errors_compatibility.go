package extensionscontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/zhuchunshu/sforum/apps/api/app/Http"
	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
)

// mapCoreIncompatibleError 把清单与 core 的兼容拒绝拆成两个状态码：
// 安装是「这个包本身不可用」（422），运行阶段是「当前状态冲突」（409）。
// 结构化字段让管理员直接看到声明范围与实际宿主版本，而不是一句笼统的失败。
func mapCoreIncompatibleError(err error) error {
	var incompatible *extensions.CoreIncompatibleError
	if !errors.As(err, &incompatible) {
		return nil
	}
	status := fiber.StatusConflict
	if incompatible.Stage == extensions.CoreCompatibilityStageInstall {
		status = fiber.StatusUnprocessableEntity
	}
	return apphttp.NewErrorWithFields(status, extensions.CodeCoreIncompatible, map[string][]string{
		"required":     {incompatible.Required},
		"coreVersion":  {incompatible.CoreVersion},
		"buildVersion": {incompatible.BuildVersion},
	})
}

// mapExecutablePlatformError 把「后端可执行文件不是本平台构建的」拆成同样的
// 两个状态码。required/host/entry 让管理员与商城安装桥直接看出该换哪个包，
// 而不是笼统地报一句安装失败。
func mapExecutablePlatformError(err error) error {
	var mismatch *extensions.ExecutablePlatformMismatchError
	if !errors.As(err, &mismatch) {
		return nil
	}
	status := fiber.StatusConflict
	if mismatch.Stage == extensions.CoreCompatibilityStageInstall {
		status = fiber.StatusUnprocessableEntity
	}
	return apphttp.NewErrorWithFields(status, extensions.CodePlatformIncompatible, map[string][]string{
		"required": {mismatch.Required},
		"host":     {mismatch.Host},
		"entry":    {mismatch.Entry},
	})
}

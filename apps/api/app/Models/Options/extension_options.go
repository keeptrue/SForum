package options

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// NameExtensionBuiltinAutoUpdate 控制 Host 是否在启动时自动升级受保护内置插件。
// 默认开启：全新安装和常规升级不需要运营理解 staged artifact 才能用上随发行版
// 提供的内置插件。关闭后仍保留管理端的手工“全部升级”路径。
const NameExtensionBuiltinAutoUpdate = "extension.builtin_auto_update"

func init() {
	optionDefinitions = append(optionDefinitions, optionDefinition{
		name:             NameExtensionBuiltinAutoUpdate,
		managePermission: identity.PermissionExtensionPluginManage,
	})
}

// BuiltinAutoUpdateEnabled 读取自动更新开关；缺失或非法值按推荐默认开启处理。
// 以自由函数而不是 Service receiver 提供，避免 Options Service 的 receiver 方法
// 继续增长（架构棘轮）。
func BuiltinAutoUpdateEnabled(ctx context.Context, service *Service) (bool, error) {
	if service == nil {
		return true, nil
	}
	values, err := service.InternalValues(ctx)
	if err != nil {
		return false, err
	}
	value, ok := normalizeEnabledOption(values[NameExtensionBuiltinAutoUpdate])
	if !ok {
		return true, nil
	}
	return value == enabledOptionValue(true), nil
}

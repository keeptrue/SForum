package profilecontroller

import (
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

// decodeSettingsMeta 解析 PUT /profile 的 meta。
//
// 四个约定，前端按此实现：
//   - 键是 entity-meta 组合键（x.<extension>.<key>），与服务端下发的
//     settingsSections[].fields[].fieldKey 完全一致
//   - 值是 string | number | boolean | null；null 表示删除该字段
//   - 只接受标量：对象/数组不是字段值，直接按非法值报错（不猜、不字符串化）
//   - 非法值不会中断整批解析：能定位到字段的就带字段错误返回 422，
//     前端保留 dirty 状态并把消息贴到对应输入框
func decodeSettingsMeta(raw map[string]json.RawMessage) (map[string]any, map[string][]string) {
	if len(raw) == 0 {
		return nil, nil
	}
	values := make(map[string]any, len(raw))
	fieldErrors := make(map[string][]string)
	for key, value := range raw {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		trimmed := strings.TrimSpace(string(value))
		if trimmed == "null" || trimmed == "" {
			values[key] = nil
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			fieldErrors[key] = []string{profile.MessageSettingsFieldInvalid}
			continue
		}
		switch decoded.(type) {
		case string, float64, bool:
			values[key] = decoded
		default:
			fieldErrors[key] = []string{profile.MessageSettingsFieldInvalid}
		}
	}
	if len(fieldErrors) > 0 {
		return nil, fieldErrors
	}
	return values, nil
}

func mapSettingsFieldError(err error) error {
	fieldErr, ok := err.(*profile.SettingsFieldError)
	if !ok {
		return nil
	}
	return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, profile.CodeSettingsInvalid, fieldErr.Fields)
}

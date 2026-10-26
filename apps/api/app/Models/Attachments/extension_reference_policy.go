package attachments

import (
	"context"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
)

// ExtensionAttachmentPolicy 解析扩展认领附件时的有效约束。
//
// 约束来自站点配置（attachment.max_file_size_mb 与
// attachment.extension_reference_limit），并由 attachments/ExtensionReference
// 归一：图片上限取站点上限与 512 KiB 的较小值，引用上限有硬顶。
func (s *Service) ExtensionAttachmentPolicy(ctx context.Context) (extensionreference.Policy, error) {
	if s == nil || s.options == nil {
		return extensionreference.PolicyFromValues(nil), nil
	}
	values, err := s.options.InternalValues(ctx)
	if err != nil {
		return extensionreference.Policy{}, err
	}
	return extensionreference.PolicyFromValues(values), nil
}

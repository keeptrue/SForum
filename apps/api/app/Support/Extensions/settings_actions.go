package extensionsruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

type fixedPluginSettings map[string]string

func (settings fixedPluginSettings) ListSettings(context.Context, string) (map[string]string, error) {
	copy := make(map[string]string, len(settings))
	for key, value := range settings {
		copy[key] = value
	}
	return copy, nil
}

// ProbeSettingsAction starts an isolated short-lived RPC runtime. It receives no Host API
// token and is never registered in Manager routes, hooks, jobs, schedules, or providers.
//
// 两条探测路径共用一个方法：provider_probe 走 provider slot，ai_tool_probe 走
// 插件自己声明的 AI 工具（保留槽 sforum.ai.tool/invoke）。请求对象化是为了不
// 触碰 RuntimeInvoker 的接收者方法数基线。
func (m *RuntimeInvoker) ProbeSettingsAction(ctx context.Context, extension extensions.Extension, request extensions.SettingsActionProbeRequest) (extensions.SettingsActionProbeResult, error) {
	starter := settingsActionProbeStarter(m.starter, request.Values)
	if _, err := starter.Start(ctx, extension); err != nil {
		return extensions.SettingsActionProbeResult{}, err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = starter.Stop(stopCtx, extension)
	}()
	if request.AIToolID != "" {
		return probeSettingsActionAITool(ctx, starter, extension, request)
	}
	if request.ProviderSlot == "attachment.storage.provider" {
		response, err := starter.StorageProbe(ctx, extension.ID, StorageProbeRequest{})
		return extensions.SettingsActionProbeResult{OK: response.OK, Reason: response.Reason, Message: response.Message}, err
	}
	response, err := starter.ProviderProbe(ctx, extension.ID, ProviderProbeRequest{Slot: request.ProviderSlot})
	return extensions.SettingsActionProbeResult{OK: response.OK, Reason: response.Reason, Message: response.Message, Details: response.Details, Suggestions: response.Suggestions}, err
}

// settingsActionProbeStarter 构造设置页探测用的短命 starter，并继承活动运行时的
// 信任来源。
//
// 继承不是可选的：protocolV2ClientConfig 对「没有信任来源且不是内置」的扩展直接
// 返回 ErrTrustGrantNotFound，而不继承的探测 starter 恰好落在这个分支里。结果是
// 只有内置插件能探测——刚好把最需要「先测再启用」的上传插件排除在外，而它的失败
// 表现只是一句「设置操作暂不可用」，没有任何线索指向信任来源。
//
// 反过来也不能给探测更松的口径：它跑的是同一份可执行制品，如果探测比正式运行时
// 更容易启动，运营者就得到了一条不需要信任即可执行插件代码的通道。
//
// 写成自由函数而不是接收者方法：RuntimeInvoker 的接收者方法数有不可增长的基线。
func settingsActionProbeStarter(active Starter, values map[string]string) *ProtocolStarter {
	config := ProtocolStarterConfig{Settings: fixedPluginSettings(values)}
	if protocolStarter, ok := active.(*ProtocolStarter); ok {
		config.Trust = protocolStarter.trust
	}
	return NewProtocolStarter(config)
}

// probeSettingsActionAITool 调用插件自己声明的 AI 工具来回答一次设置页探测。//
// 超时必须逐字等于声明的 timeoutMs（宿主拒绝任何漂移）；arguments 是草稿值的
// JSON 编码——插件从进程 env 读自己的配置，因此参数与 env 表达的是同一份事实。
func probeSettingsActionAITool(
	ctx context.Context,
	starter *ProtocolStarter,
	extension extensions.Extension,
	request extensions.SettingsActionProbeRequest,
) (extensions.SettingsActionProbeResult, error) {
	var declaration *extensions.ManifestAITool
	for index := range extension.Manifest.AITools {
		if extension.Manifest.AITools[index].ID == request.AIToolID {
			declaration = &extension.Manifest.AITools[index]
			break
		}
	}
	if declaration == nil {
		return extensions.SettingsActionProbeResult{}, fmt.Errorf("%w: ai tool %q is not declared", extensions.ErrSettingsActionUnavailable, request.AIToolID)
	}
	arguments, err := json.Marshal(request.Values)
	if err != nil {
		return extensions.SettingsActionProbeResult{}, fmt.Errorf("%w: %v", extensions.ErrSettingsActionUnavailable, err)
	}
	response, err := starter.InvokeVersionedAITool(ctx, extension, VersionedAIToolRequest{
		DeclarationID:   declaration.ID,
		ContractVersion: declaration.ContractVersion,
		Handler:         declaration.Handler,
		Name:            declaration.Name,
		Timeout:         time.Duration(declaration.TimeoutMS) * time.Millisecond,
		Arguments:       string(arguments),
	})
	if err != nil {
		return extensions.SettingsActionProbeResult{}, err
	}
	if response.IsError {
		return extensions.SettingsActionProbeResult{OK: false, Reason: "extension.settings_action_ai_tool_failed", Message: response.Content}, nil
	}
	return extensions.SettingsActionProbeResult{OK: true, Reason: "extension.settings_action_ai_tool_ok", Message: response.Content}, nil
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) ProbeSettingsAction(ctx context.Context, extension extensions.Extension, request extensions.SettingsActionProbeRequest) (extensions.SettingsActionProbeResult, error) {
	return m.invoker.ProbeSettingsAction(ctx, extension, request)
}

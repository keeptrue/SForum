package ai

import (
	"context"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// ListModels 列出某个供应商端点当前可用的模型，供控制台一键选择。
//
// 它刻意不走网关：这不是一次补全调用，没有 token、没有费用，也不该出现在执行
// 记录里——否则「获取模型列表」会污染成本口径与用量看板。出站仍然走同一个受控
// 客户端，SSRF 校验、超时与响应体上限因此完全一致。
//
// 供应商层面的失败作为结论返回（Reason/Error），而不是错误：地址能连上但密钥
// 无效，是运营者要读到的信息，不是服务端故障。
func (s *Service) ListModels(
	ctx context.Context,
	actor identity.Actor,
	profileID string,
	probe supportai.ModelListProbe,
) (supportai.ModelListResult, error) {
	if !actor.Can(identity.PermissionAIManage) {
		return supportai.ModelListResult{}, identity.ErrPermissionDenied
	}
	if s == nil || s.settings == nil || s.models == nil {
		return supportai.ModelListResult{}, supportai.ErrModelListUnavailable
	}
	settings, err := s.settings.GetSettings(ctx)
	if err != nil {
		return supportai.ModelListResult{}, err
	}
	profileID = strings.TrimSpace(profileID)
	profile, ok := settings.Profile(profileID)
	if !ok {
		// 尚未保存的自定义供应商。允许用请求里的地址做一次性探测：否则「先填地址
		// 和密钥，再挑模型」这条最自然的路径，必须先把一份没有模型的配置存进库。
		if strings.TrimSpace(probe.BaseURL) == "" {
			return supportai.ModelListResult{}, supportai.ErrProfileUnavailable
		}
		profile = supportai.Profile{ID: profileID, Protocol: supportai.ProtocolOpenAIChat}
	}
	target, err := profile.WithProbe(probe)
	if err != nil {
		return supportai.ModelListResult{}, err
	}
	apiKey := strings.TrimSpace(probe.APIKey)
	if apiKey == "" {
		apiKey, err = s.storedCredential(ctx, target.APIKeyRef)
		if err != nil {
			return supportai.ModelListResult{}, err
		}
	}
	result := supportai.ModelListResult{
		ProfileID: target.ID,
		Protocol:  target.Protocol,
	}
	list, err := s.models.ListModels(ctx, target, apiKey)
	if err != nil {
		// 出站守卫拒绝与供应商失败是两种结论：前者要改地址，后者要查网络或密钥。
		result.Reason = supportai.ReasonForProviderFailure(err)
		result.Error = err.Error()
		return result, nil
	}
	result.ModelList = list
	return result, nil
}

// storedCredential 解析已保存的密钥。空值、缺失的解析器与解析失败一律归为
// 「密钥缺失」：对运营者而言「站点没配密钥」和「密钥读不出来」要做的是同一件事，
// 而探测本来就不该在无凭证时发出请求。
func (s *Service) storedCredential(ctx context.Context, reference string) (string, error) {
	if s.reader == nil {
		return "", supportai.ErrCredentialMissing
	}
	apiKey, err := s.reader.ResolveAIKey(ctx, reference)
	if err != nil {
		return "", supportai.ErrCredentialMissing
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", supportai.ErrCredentialMissing
	}
	return apiKey, nil
}

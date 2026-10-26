package pluginv2

import (
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 同意式读取（consent-gated）的稳定合同。
//
// 这个读取面只有一条合同：**调用者本人**的主邮箱。它由 Host 在宿主发起的
// route/admin 调用里签发一次性 query actor 委派，插件凭该委派读取；插件请求
// 里没有任何 userId 参数，因此不存在「读别人」的形状。
//
// 与公开稳定查询（site stats、公开主题等）的区别：
//   - 必须用 Host.DelegatedQueryRequest / Host.SelfEmailQueryRequest，不能裸调；
//   - manifest 必须声明 users.email.read，否则 Host 不会签发委派；
//   - 结果永不缓存，每次读取留一条审计（不含值）；
//   - 账号无 email 或非 active 时返回空行集合，不报错。
const (
	SelfEmailQueryID             = "core.query.identity.self_email"
	SelfEmailContractVersion     = SelfEmailQueryID + "@1"
	SelfEmailPlanVersion         = SelfEmailQueryID + ".plan@1"
	SelfEmailResultSchemaID      = "sforum.core.self_email"
	SelfEmailResultSchemaVersion = "1"
	SelfEmailScope               = "route"
	SelfEmailQueryPageLimit      = 1
)

// SelfEmailQueryRequest builds the delegated single-row request for the actor's
// own primary email. It fails closed with ErrHostQueryDelegationUnavailable when
// the current invocation carries no matching Host-issued delegation (anonymous
// callers, calls from a job/schedule, or an artifact without users.email.read).
func (h *Host) SelfEmailQueryRequest(parent *protocolwire.RequestContext) (*hostwire.QueryRequest, error) {
	request, err := h.DelegatedQueryRequest(parent, SelfEmailQueryID, SelfEmailContractVersion, SelfEmailPlanVersion)
	if err != nil {
		return nil, err
	}
	request.Page = &protocolwire.PageRequest{Limit: SelfEmailQueryPageLimit}
	return request, nil
}

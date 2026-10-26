package ai

import (
	"errors"

	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
)

// DiagnoseResult 是一次连通性探测的结论。失败时带上足够定位问题的信息，
// 而不是一个笼统的错误——诊断的价值全在这里。
type DiagnoseResult struct {
	OK         bool   `json:"ok"`
	ProviderID string `json:"providerId,omitempty"`
	Model      string `json:"model,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	// CostClass 说明这次探测走的是哪个成本等级的绑定。运营者可能只绑定了
	// standard/premium，把它回传，界面才不会让人以为诊断测的就是全部配置。
	CostClass string `json:"costClass,omitempty"`
	// Reply 是模型返回的正文。它刻意不带 omitempty：调用成功但正文为空时，
	// 前端必须能区分「空字符串」与「字段缺失」，否则只能渲染出一条空白条。
	Reply        string `json:"reply"`
	LatencyMS    int64  `json:"latencyMs,omitempty"`
	InputTokens  int    `json:"inputTokens,omitempty"`
	OutputTokens int    `json:"outputTokens,omitempty"`
	// StopReason 是协议无关的停止原因。正文为空时它是唯一能解释「为什么一个字
	// 都没有」的线索：length 表示输出预算被耗尽（推理模型常见），filtered 表示
	// 被内容过滤拦下。
	StopReason string `json:"stopReason,omitempty"`
	// Reason 是稳定原因码，供前端选择文案；Error 是给人看的说明。
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
}

// 诊断结论的原因码。前三项是配置问题，不需要真的发请求就能判定。
const (
	DiagnoseReasonDisabled     = "gateway_disabled"
	DiagnoseReasonNoProvider   = "profile_unavailable"
	DiagnoseReasonNoCredential = "credential_missing"
	// DiagnoseReasonEmptyReply 表示请求已经成功走完，但模型没有返回任何正文。
	// 它不是失败，却也不是「连接正常」：推理模型会先把输出预算消耗在思考上，
	// 64 token 的预算几乎必然被耗尽，于是拿到一个成功的空回复。把它单列出来，
	// 运营者才知道该调预算而不是反复重试。
	DiagnoseReasonEmptyReply = "empty_reply"
	// DiagnoseReasonProviderFailed 与 ReasonProviderFailed 同一取值：诊断与模型
	// 列表探测共用一套出站失败原因码，前端因此只需要一份文案。
	DiagnoseReasonProviderFailed = ReasonProviderFailed
)

// 出站失败的原因码。诊断与模型列表探测共用。
const (
	// ReasonProviderFailed 表示请求已经发出但没有成功（网络、鉴权、限流等）。
	ReasonProviderFailed = "provider_failed"
	// ReasonUnsafeTarget 表示出站守卫拒绝了目标地址：scheme 不被允许，或它解析到
	// 私网/回环/保留地址。运营者要改的是地址，而不是重试——所以它必须与
	// provider_failed 区分开。
	ReasonUnsafeTarget = "unsafe_target"
)

// ReasonForProviderFailure 把一次出站失败归类成稳定原因码。
func ReasonForProviderFailure(err error) string {
	if errors.Is(err, outboundhttp.ErrUnsafeURL) {
		return ReasonUnsafeTarget
	}
	return ReasonProviderFailed
}

// ReplyBotInfo 描述当前充当 AI 助手的账号。它让控制台能回答「到底用哪个账号
// 发言」，而不是让运营者去猜。
type ReplyBotInfo struct {
	UserID   int64  `json:"userId,omitempty"`
	Username string `json:"username,omitempty"`
	// Configured 为 false 表示站内还没有机器人账号，回复功能不会生效。
	Configured bool `json:"configured"`
}

// DiagnosePurpose 是探测调用的用途标识。它出现在执行记录里，让运维探测与
// 真实业务调用在用量表中可区分。
const DiagnosePurpose = "admin.connectivity_test"

// DefaultDiagnoseMessage 是未指定内容时的探测消息：越短越省钱，且要求一个
// 确定性的短回复，便于人工一眼判断是否正常。
const DefaultDiagnoseMessage = "请只回复两个字：收到"

// DiagnosePromptVersion 参与缓存键与执行记录，改动探测提示词时递增。
// diagnose@3：网关不再向 system 追加任何宿主文本；探测用途实际生效的系统指令
// 回到探测消息本身。
// diagnose@2：网关开始注入自我身份声明，探测用途实际生效的系统指令变了。
const DiagnosePromptVersion = "diagnose@3"

// DiagnoseMaxTokens 是探测调用的输出预算。它刻意比「请只回复两个字」所需的
// token 多得多：推理类模型会先把预算消耗在思考上，64 个 token 几乎必然被耗尽，
// 于是一次配置正确的探测也会返回空正文——那正是这次要修掉的假故障。
const DiagnoseMaxTokens = 512

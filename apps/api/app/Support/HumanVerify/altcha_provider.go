package humanverify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
)

type AltchaConfig struct {
	Secret       string
	Cost         int
	ChallengeTTL time.Duration
}

type AltchaProvider struct {
	secret       string
	cost         int
	challengeTTL time.Duration
}

func NewAltchaProvider(cfg AltchaConfig) *AltchaProvider {
	if cfg.Cost <= 0 {
		cfg.Cost = 1000
	}
	if cfg.ChallengeTTL == 0 {
		cfg.ChallengeTTL = 10 * time.Minute
	}
	return &AltchaProvider{
		secret:       cfg.Secret,
		cost:         cfg.Cost,
		challengeTTL: cfg.ChallengeTTL,
	}
}

// challengePurposeDataKey 是 ALTCHA parameters.data 里承载签发场景的字段名。
const challengePurposeDataKey = "purpose"

func (p *AltchaProvider) Challenge(_ context.Context, purpose Purpose, _ Subject) (Challenge, error) {
	expiresAt := time.Now().Add(p.challengeTTL)
	challenge, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:           "PBKDF2/SHA-256",
		Cost:                p.cost,
		ExpiresAt:           &expiresAt,
		HMACSignatureSecret: p.secret,
		// purpose 写进 parameters.data：data 属于 HMAC 签名的规范化载荷，客户端
		// 既改不动也剥不掉，因此 token 与签发场景强绑定（P3-4 第二步）。
		Data: map[string]interface{}{challengePurposeDataKey: string(purpose)},
	})
	if err != nil {
		return Challenge{}, err
	}
	return Challenge{Provider: ProviderAltcha, Purpose: purpose, Payload: challenge}, nil
}

// Verify 校验解出的 ALTCHA payload，并比对签发场景。
//
// 场景绑定：Challenge 把 purpose 写进 parameters.data，而 parameters（含 data）
// 是签名载荷的一部分，改动 data 会让签名失配。所以「data.purpose != req.Purpose」
// 就是跨场景复用，直接按无效拒绝。
//
// 兼容分支（限时）：本次发布之前签发的 challenge 没有 data（签名同样有效，只是不含
// 场景信息），其 TTL 上限是 ChallengeTTL，会自然过期。这里放行而不是拒绝，避免滚动
// 发布窗口内已经打开的表单全部失败。放行不构成绕过——签名覆盖 data，攻击者无法从一个
// 带 data 的 token 上把它剥掉（TestAltchaProviderRejectsStrippedChallengeDataSignature
// 钉住「签名覆盖 data」，场景比对本身由 TestAltchaProviderBindsChallengeToPurpose 守）。
//
// 该分支的失效时间点、移除条件与删除范围见
// knowledge/decisions/2026-11-16-altcha-purpose-binding-rollout-window.md §3
// （2026-11-30 之后或下一个 minor 版本，先到者）。
func (p *AltchaProvider) Verify(_ context.Context, req VerifyRequest) (VerifyResult, error) {
	payload, err := decodePayload(req.Token)
	if err != nil {
		return VerifyResult{Verified: false, Code: CodeInvalid}, nil
	}

	result, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:           payload.Challenge,
		Solution:            payload.Solution,
		DeriveKey:           altcha.DeriveKeyPBKDF2(),
		HMACSignatureSecret: p.secret,
	})
	if err != nil {
		return VerifyResult{}, err
	}
	if result.Expired {
		return VerifyResult{Verified: false, Code: CodeExpired}, nil
	}
	if result.InvalidSignature != nil && *result.InvalidSignature {
		return VerifyResult{Verified: false, Code: CodeInvalid}, nil
	}
	if result.InvalidSolution != nil && *result.InvalidSolution {
		return VerifyResult{Verified: false, Code: CodeInvalid}, nil
	}
	if !result.Verified {
		return VerifyResult{Verified: false, Code: CodeInvalid}, nil
	}
	if bound, ok := boundPurpose(payload.Challenge.Parameters.Data); ok && bound != string(req.Purpose) {
		return VerifyResult{Verified: false, Code: CodeInvalid}, nil
	}
	return VerifyResult{Verified: true, Code: CodeOK}, nil
}

// boundPurpose 取出签名载荷里的签发场景。
// ok=false 表示这是发布前签发、没有场景绑定的历史 challenge（见 Verify 注释）；
// 字段存在但不是字符串时返回 ("", true)，即按不匹配处理，绝不因为形态异常放行。
func boundPurpose(data map[string]interface{}) (string, bool) {
	value, ok := data[challengePurposeDataKey]
	if !ok {
		return "", false
	}
	purpose, _ := value.(string)
	return purpose, true
}

func decodePayload(token string) (altcha.Payload, error) {
	body, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return altcha.Payload{}, err
	}

	var payload altcha.Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		return altcha.Payload{}, err
	}
	return payload, nil
}

package identity

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// silentTrustSource 是本地 fake：不 import trustlevels（内部测试包禁止，
// 会形成 import cycle）。
type silentTrustSource struct {
	err error
}

func (s silentTrustSource) Grants(context.Context, int64, UserKind, time.Time) (int, []string, error) {
	return 0, nil, s.err
}

// TestResolveTrustGrantsDisabledIsSilent 锁定 D9/F 组：关闭态必须静默。
//
// 关闭态如果写了 warn，每个已认证请求就是一条日志——线上等于日志洪水，而且运营会
// 以为出了故障。真实错误仍然必须写 warn（对照组，T10）。
func TestResolveTrustGrantsDisabledIsSilent(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	level, earned, ok := resolveTrustGrants(context.Background(), 7, UserKindHuman, time.Now(),
		silentTrustSource{err: ErrTrustLevelsDisabled})
	if ok || level != 0 || len(earned) != 0 {
		t.Fatalf("关闭态必须返回未解析，得到 ok=%v level=%d earned=%v", ok, level, earned)
	}
	if strings.Contains(buf.String(), "resolve trust level failed") {
		t.Fatalf("关闭态写了 warn：%s", buf.String())
	}

	// 对照组：真实错误必须写 warn，否则上面对照组没有意义。
	buf.Reset()
	if _, _, ok := resolveTrustGrants(context.Background(), 7, UserKindHuman, time.Now(),
		silentTrustSource{err: context.DeadlineExceeded}); ok {
		t.Fatal("真实错误也必须返回未解析")
	}
	if !strings.Contains(buf.String(), "resolve trust level failed") {
		t.Fatalf("真实错误没有写 warn：%s", buf.String())
	}
}

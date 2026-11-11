package trustlevels_test

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 这两个基准回答一个具体问题：资料页每次请求把 trust.levels 解一次，代价多大。
//
// 背景：后台读面/资料页等级卡需要档位的 id + 双语名，而 options 侧的缓存只缓存
// **归一化后的 JSON 字符串**（30s TTL 的进程内 map），没有解码结果缓存。所以
// 每次请求都会重新走一遍 DecodeConfig——这里量出真实数字，供后续决定要不要
// 加一层解码缓存（本次刻意不优化）。
//
// 运行：cd apps/api && go test ./app/Models/TrustLevels/ -bench BenchmarkDecodeConfig -benchmem

func BenchmarkDecodeConfigDefault(b *testing.B) {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		b.Fatalf("EncodeConfig: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := trustlevels.DecodeConfig(encoded); err != nil {
			b.Fatalf("DecodeConfig: %v", err)
		}
	}
}

func BenchmarkDecodeConfigEightTiers(b *testing.B) {
	encoded := newTiersJSON(trustlevels.MaxTiers)
	if _, err := trustlevels.DecodeConfig(encoded); err != nil {
		b.Fatalf("前置条件失败：8 档配置不可解码：%v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := trustlevels.DecodeConfig(encoded); err != nil {
			b.Fatalf("DecodeConfig: %v", err)
		}
	}
}

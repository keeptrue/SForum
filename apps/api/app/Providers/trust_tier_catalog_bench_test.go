package providers

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// eightTierPolicyJSON 是 8 档（上限）的 trust.levels 值，用来量最坏情况的解码量。
const eightTierPolicyJSON = `{"schemaVersion":2,"tiers":[
	{"id":"member","name":{"zh":"正式用户","en":"Member"},"icon":"leaf","requirements":{"days_visited":3,"comment_count":5},"limits":{},"earnable":[]},
	{"id":"resident","name":{"zh":"常驻用户","en":"Resident"},"icon":"home","requirements":{"days_visited":15,"topic_count":2,"comment_count":20,"like_received_count":5,"like_given_count":3},"limits":{},"earnable":[]},
	{"id":"veteran","name":{"zh":"资深用户","en":"Veteran"},"icon":"mountain","requirements":{"days_visited":45,"topic_count":8,"comment_count":80,"like_received_count":30,"like_given_count":20,"bookmark_count":5},"limits":{},"earnable":[]},
	{"id":"leader","name":{"zh":"社区领袖","en":"Leader"},"icon":"crown","requirements":{"days_visited":100,"topic_count":25,"comment_count":250,"like_received_count":100,"like_given_count":80,"bookmark_count":20},"limits":{},"earnable":[]},
	{"id":"","name":{"zh":"活跃贡献者","en":"Contributor"},"icon":"star","requirements":{"days_visited":150},"limits":{},"earnable":[]},
	{"id":"","name":{"zh":"核心贡献者","en":"Core Contributor"},"icon":"sun","requirements":{"days_visited":200},"limits":{},"earnable":[]},
	{"id":"","name":{"zh":"元老","en":"Elder"},"icon":"moon","requirements":{"days_visited":300},"limits":{},"earnable":[]},
	{"id":"","name":{"zh":"传奇","en":"Legend"},"icon":"flame","requirements":{"days_visited":365},"limits":{},"earnable":[]}]}`

// 这两个基准量的是**资料页热路径**的真实成本：`publicTrustLevel` 每次都问一次
// 档位身份（id + 双语名），而身份来自 trust.levels。
//
// 两段成本要分开看：
//   - WebOption：options 侧有 30s TTL 的进程内缓存，命中时只加锁 + 复制一份归一化
//     后的字符串 map，不碰数据库；
//   - DecodeConfig：**没有**记忆化，每次都要把整份 tiers JSON 解一遍。
//
// 所以下面这个基准 = 缓存命中的 WebOption + 一次完整解码。
//
// 运行：cd apps/api && go test ./app/Providers/ -bench BenchmarkTrustTierCatalog -benchmem

// catalogBenchStore 是最小的 options.Store 实现：只服务缓存预热那一次 List。
type catalogBenchStore struct {
	items     map[string]string
	listCalls int
}

func (s *catalogBenchStore) List(context.Context) ([]options.Option, error) {
	s.listCalls++
	rows := make([]options.Option, 0, len(s.items))
	for name, value := range s.items {
		rows = append(rows, options.Option{Name: name, Value: value})
	}
	return rows, nil
}

func (s *catalogBenchStore) InsertMissing(_ context.Context, input options.UpdateInput) error {
	if _, ok := s.items[input.Name]; !ok {
		s.items[input.Name] = input.Value
	}
	return nil
}

func (s *catalogBenchStore) Upsert(_ context.Context, input options.UpdateInput) (options.Option, error) {
	s.items[input.Name] = input.Value
	return options.Option{Name: input.Name, Value: input.Value}, nil
}

func catalogBenchActor() identity.Actor {
	return identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTrustManage: true},
	}
}

// newCachedCatalog 造一个缓存已预热的 catalog（基准循环里不会再碰 store）。
func newCachedCatalog(b *testing.B) (trustTierCatalog, *catalogBenchStore) {
	b.Helper()
	store := &catalogBenchStore{items: map[string]string{}}
	service := options.NewServiceWithDefaults(store, options.Defaults{})
	if err := service.EnsureDefaults(context.Background()); err != nil {
		b.Fatalf("EnsureDefaults: %v", err)
	}
	if _, err := service.Update(context.Background(), catalogBenchActor(), options.UpdateInput{
		Name:  options.NameTrustLevels,
		Value: eightTierPolicyJSON,
	}); err != nil {
		b.Fatalf("Update(trust.levels): %v", err)
	}
	catalog := trustTierCatalog{options: service}
	// 预热 options 的 30s 缓存（第一次 WebOption 会走 store）。
	if _, ok := catalog.TierIdentity(context.Background(), 2); !ok {
		b.Fatal("预热失败：TierIdentity 没返回身份")
	}
	return catalog, store
}

// assertNoStoreRead 证明基准量的是**缓存命中**路径：options 的 30s 进程内缓存
// 生效时，一次字典读取都不该打库。
func assertNoStoreRead(b *testing.B, store *catalogBenchStore) {
	b.Helper()
	b.StopTimer()
	if store.listCalls != 0 {
		b.Fatalf("基准期间打了 %d 次 store.List：缓存没生效，数字不能代表热路径", store.listCalls)
	}
}

// BenchmarkTrustTierCatalogCached 是资料页实际调用的完整路径（缓存命中 + 解码）。
func BenchmarkTrustTierCatalogCached(b *testing.B) {
	catalog, store := newCachedCatalog(b)
	listCallsBefore := store.listCalls
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := catalog.TierIdentity(context.Background(), 3); !ok {
			b.Fatal("TierIdentity 没返回身份")
		}
	}
	b.StopTimer()
	if store.listCalls != listCallsBefore {
		b.Fatalf("基准期间打了 %d 次 store.List：缓存没生效", store.listCalls-listCallsBefore)
	}
	b.StartTimer()
}

// BenchmarkTrustTierCatalogWebOptionOnly 只量缓存命中的 WebOption（不含解码），
// 用来把总成本拆成「options 侧」与「DecodeConfig」两段。
func BenchmarkTrustTierCatalogWebOptionOnly(b *testing.B) {
	catalog, store := newCachedCatalog(b)
	listCallsBefore := store.listCalls
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := catalog.options.WebOption(ctx, options.NameTrustLevels); err != nil {
			b.Fatalf("WebOption: %v", err)
		}
	}
	b.StopTimer()
	if store.listCalls != listCallsBefore {
		b.Fatalf("基准期间打了 %d 次 store.List：缓存没生效", store.listCalls-listCallsBefore)
	}
	b.StartTimer()
}

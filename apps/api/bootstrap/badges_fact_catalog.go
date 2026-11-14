package bootstrap

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// badgesFactCatalog 是「当前已启用插件声明的事实条件」的候选来源。
//
// 为什么需要一层缓存：`conditions.FactCandidateProvider` 是无 ctx 的同步函数，而
// 求值路径（每次用户评估）都会读它。直接查库等于每评估一个用户多一条 SELECT；
// 回扫时就是一个用户一条，全站回扫能把它放大成几万次查询。
//
// 缓存策略刻意是「短 TTL + 失败回退上次快照」：
//   - TTL 5s：扩展启停/卸载后最多 5 秒内候选仍旧，期间求值按旧候选进行。这与
//     「未知事实 → unknown」的语义相容（unknown 不授予，最坏是延迟 5 秒恢复授予）；
//   - 失败回退：一次 DB 抖动不该让全站评估瞬间退化成「所有插件条件 unknown」，
//     那会暂停所有插件事实徽章的授予。旧快照宁可稍旧，也不要制造一次全站静默停发。
type badgesFactCatalog struct {
	lister EnabledPackageLister
	logger *slog.Logger

	mu       sync.Mutex
	loadedAt time.Time
	snapshot []conditions.FactCandidate
	// byExtension 是「扩展 id → 它自己声明的 factKey」，服务 Host 命令的
	// 所有权校验：前缀只能证明命名空间，证明不了「这条事实真被该扩展声明过」。
	byExtension map[string][]string
	// earnable 是「扩展声明的可被等级授予的权限键」（含停用扩展：区分 inactive 与
	// unknown 全靠它）。与事实候选共用同一份快照——同一个 5s 窗口内两处答案必须一致。
	earnable []trustlevels.EarnableDeclaration
	loaded   bool
}

// EnabledPackageLister 是本文件需要的唯一扩展读口（*extensions.Service 满足它）。
type EnabledPackageLister interface {
	ListPackagesWithoutActor(ctx context.Context) ([]extensions.Extension, error)
}

// badgesFactCatalogTTL 是候选快照的有效期。
const badgesFactCatalogTTL = 5 * time.Second

// badgesFactCatalogTimeout 限制刷新查询的时长：这是求值路径上的同步调用，不能被
// 一次慢查询拖住整页回扫。
const badgesFactCatalogTimeout = 2 * time.Second

func newBadgesFactCatalog(lister EnabledPackageLister, logger *slog.Logger) *badgesFactCatalog {
	return &badgesFactCatalog{lister: lister, logger: logger}
}

// Candidates 返回当前候选（实现 conditions.FactCandidateProvider）。
func (c *badgesFactCatalog) Candidates() []conditions.FactCandidate {
	if c == nil || c.lister == nil {
		return []conditions.FactCandidate{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && time.Since(c.loadedAt) < badgesFactCatalogTTL {
		return c.snapshot
	}
	ctx, cancel := context.WithTimeout(context.Background(), badgesFactCatalogTimeout)
	defer cancel()
	packages, err := c.lister.ListPackagesWithoutActor(ctx)
	if err != nil {
		// 失败回退旧快照：见类型注释（宁可稍旧，也不要一次抖动让全站停发）。
		if c.logger != nil {
			c.logger.Warn("badges fact catalog refresh failed; keeping last snapshot", "error", err)
		}
		if c.loaded {
			return c.snapshot
		}
		return []conditions.FactCandidate{}
	}
	candidates, conflicts, byExtension := mergeEnabledFactDeclarationsDetailed(packages)
	if c.logger != nil && len(conflicts) > 0 {
		for _, conflict := range conflicts {
			// 冲突进日志而不是静默丢弃：被拒的声明意味着某插件「以为声明成功了」。
			c.logger.Warn("badges fact declaration rejected",
				"key", conflict.Key, "extension_id", conflict.ExtensionID, "reason", conflict.Reason)
		}
	}
	c.snapshot = candidates
	c.byExtension = byExtension
	c.earnable = earnableDeclarationsFromPackages(packages)
	c.loadedAt = time.Now()
	c.loaded = true
	return c.snapshot
}

// BadgeFactKeys 返回某扩展自己声明的 factKey（Host 命令的所有权校验读口）。
//
// 只回**已启用插件**的声明：扩展停用后它就不能再上报事实了（写入会被拒），
// 这样「停用」与「不再产生新数据」是同一件事。
func (c *badgesFactCatalog) BadgeFactKeys(_ context.Context, extensionID string) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	// 复用 Candidates 的刷新与缓存：同一份快照既喂目录也喂所有权校验，
	// 两处给出不同答案会让「目录里有、写入却被拒」这种半死状态出现。
	c.Candidates()
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := c.byExtension[extensionID]
	out := make([]string, len(keys))
	copy(out, keys)
	return out, nil
}

// FactKeys 是候选的 key 列表（喂 resolver 与 rule_health）。
func (c *badgesFactCatalog) FactKeys() []string {
	return conditions.FactKeys(c.Candidates())
}

// mergeEnabledFactDeclarations 只取**已启用插件**的声明。
//
// 只认 `StatusEnabled` 与 `TypePlugin`：停用/卸载会让规则里的 factKey 立刻失去候选，
// 求值转 unknown（spec §4.1）。主题不能声明事实（校验器 rule #1 已挡，这里再挡一次，
// 因为装配层见过的数据可能来自旧版本或测试夹具）。
func mergeEnabledFactDeclarations(packages []extensions.Extension) ([]conditions.FactCandidate, []conditions.FactConflict) {
	candidates, conflicts, _ := mergeEnabledFactDeclarationsDetailed(packages)
	return candidates, conflicts
}

// mergeEnabledFactDeclarationsDetailed 额外返回「扩展 id → 已接受的事实 key」。
func mergeEnabledFactDeclarationsDetailed(packages []extensions.Extension) ([]conditions.FactCandidate, []conditions.FactConflict, map[string][]string) {
	declarations := make([]conditions.FactDeclaration, 0)
	for _, item := range packages {
		if item.Status != extensions.StatusEnabled || item.Type != extensions.TypePlugin {
			continue
		}
		// 两个数组同一份事实层：`userFactConditions` 是中性名（canonical），
		// `badgeConditions` 是兼容别名。只读一个数组，用新别名声明的插件在信任等级里
		// 就会查不到自己的条件——而且不会报错，只是那个条件永远 unknown。
		declarationsOf := make([]extensionmanifest.ManifestBadgeCondition, 0, len(item.Manifest.BadgeConditions)+len(item.Manifest.UserFactConditions))
		declarationsOf = append(declarationsOf, item.Manifest.BadgeConditions...)
		declarationsOf = append(declarationsOf, item.Manifest.UserFactConditions...)
		for _, declared := range declarationsOf {
			declarations = append(declarations, conditions.FactDeclaration{
				ExtensionID: item.Manifest.ID,
				Key:         declared.Key,
				Compare:     declared.Compare,
				LabelZh:     declared.Label["zh-CN"],
				LabelEn:     declared.Label["en-US"],
				UnitZh:      declared.Unit["zh-CN"],
				UnitEn:      declared.Unit["en-US"],
				Variants:    factVariantsFromManifest(declared),
			})
		}
	}
	candidates, conflicts := conditions.MergeFactDeclarations(declarations)
	accepted := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		accepted[candidate.Key] = struct{}{}
	}
	byExtension := make(map[string][]string)
	for _, declaration := range declarations {
		// 只回**被合并接受**的 key：被拒的声明（撞内置/重名/越界）不能用来写事实，
		// 否则「声明被拒但写入成功」会让运维永远找不到那行数据从哪来。
		if _, ok := accepted[declaration.Key]; !ok {
			continue
		}
		byExtension[declaration.ExtensionID] = append(byExtension[declaration.ExtensionID], declaration.Key)
	}
	return candidates, conflicts, byExtension
}

// earnableDeclarationsFromPackages 收集「扩展声明的可被等级授予的权限键」。
//
// **包含非启用扩展**：trustlevels 要用它区分 active（enabled）与 inactive（仅安装/
// 停用）。只传启用者会让停用插件的键在后台凭空变 unknown，运营看起来像配置坏了。
func earnableDeclarationsFromPackages(packages []extensions.Extension) []trustlevels.EarnableDeclaration {
	out := make([]trustlevels.EarnableDeclaration, 0, 8)
	for _, item := range packages {
		if item.Type != extensions.TypePlugin {
			continue
		}
		enabled := item.Status == extensions.StatusEnabled
		for _, permission := range item.Manifest.PermissionDefinitions {
			if !permission.TrustLevelEarnable {
				continue
			}
			out = append(out, trustlevels.EarnableDeclaration{
				Key:         permission.Key,
				ExtensionID: item.Manifest.ID,
				Enabled:     enabled,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].ExtensionID < out[j].ExtensionID
	})
	return out
}

// EarnableDeclarations 返回当前快照里的可授予权限声明（实现 trustlevels.EarnableSource）。
func (c *badgesFactCatalog) EarnableDeclarations(context.Context) ([]trustlevels.EarnableDeclaration, error) {
	if c == nil || c.lister == nil {
		return nil, nil
	}
	// 复用 Candidates 的刷新与缓存：同一份快照既喂事实候选也喂权限声明。
	c.Candidates()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]trustlevels.EarnableDeclaration, len(c.earnable))
	copy(out, c.earnable)
	return out, nil
}

func factVariantsFromManifest(declared extensionmanifest.ManifestBadgeCondition) []conditions.FactVariant {
	variants := make([]conditions.FactVariant, 0, len(declared.Variants))
	for _, variant := range declared.Variants {
		variants = append(variants, conditions.FactVariant{
			Value:   variant.Value,
			LabelZh: variant.Label["zh-CN"],
			LabelEn: variant.Label["en-US"],
		})
	}
	return variants
}

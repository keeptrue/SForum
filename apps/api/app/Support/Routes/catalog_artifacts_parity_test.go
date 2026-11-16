package routes

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// 生成器（scripts/v3-catalog/generate.mjs）一次写出三份路由产物：
//   - apps/api/app/Support/Routes/core_catalog_gen.go（CoreRouteCatalog()）
//   - docs/extensions/v3/catalog-identities.json（routes）
//   - docs/extensions/v3/catalogs/routes.json（routes）
//
// 历史上有六次「只更新其中一两份、手工计数没跟上」的漂移（421→423→428→433→435→437，
// 见 TestCoreRouteCatalogHasExactReviewedGuardParity 里的注释）。这里把「路由总数」
// 变成跨产物等值断言：三份同批生成才绿；任何一份单边改动都会红并报出两个数字。
const (
	catalogIdentitiesArtifact = "../../../../../docs/extensions/v3/catalog-identities.json"
	catalogRoutesArtifact     = "../../../../../docs/extensions/v3/catalogs/routes.json"
)

// catalogArtifactRoute 是三份产物共同携带的路由身份字段（Go 侧对应 CoreRoute 的
// ID/ContractVersion/Method/Path）。只取身份字段，不把整份 JSON 抄进源码。
type catalogArtifactRoute struct {
	ID              string `json:"id"`
	ContractVersion string `json:"contractVersion"`
	Method          string `json:"method"`
	Path            string `json:"path"`
}

// TestCatalogArtifactsAgreeOnRouteCount 断言生成物、identities、catalogs/routes.json
// 三者的路由条数与逐条身份一致。期望值不来自任何一个硬编码数字。
func TestCatalogArtifactsAgreeOnRouteCount(t *testing.T) {
	generated := coreRouteIdentityKeys(CoreRouteCatalog())
	identities := catalogRouteIdentityKeys(readCatalogIdentities(t))
	catalogs := catalogRouteIdentityKeys(readCatalogRoutes(t))

	// 先比条数：报「哪份产物 vs 哪份产物」两个数字，一眼看出该补哪边。
	for _, artifact := range []struct {
		name string
		keys []string
	}{
		{"catalog-identities.json", identities},
		{"catalogs/routes.json", catalogs},
	} {
		if len(generated) != len(artifact.keys) {
			t.Fatalf("路由目录计数漂移：core_catalog_gen.go = %d，%s = %d —— 三份产物必须由 scripts/v3-catalog/generate.mjs 同一批生成",
				len(generated), artifact.name, len(artifact.keys))
		}
	}

	// 再比身份：条数相同也可能一增一删（改名、换方法、改路径、只改 id 不升契约）。
	assertSameRouteIdentities(t, "core_catalog_gen.go", generated, "catalog-identities.json", identities)
	assertSameRouteIdentities(t, "core_catalog_gen.go", generated, "catalogs/routes.json", catalogs)
}

func readCatalogIdentities(t *testing.T) []catalogArtifactRoute {
	t.Helper()
	var document struct {
		Routes []catalogArtifactRoute `json:"routes"`
	}
	readCatalogArtifact(t, catalogIdentitiesArtifact, &document)
	if len(document.Routes) == 0 {
		t.Fatalf("%s 的 routes 为空", catalogIdentitiesArtifact)
	}
	return document.Routes
}

func readCatalogRoutes(t *testing.T) []catalogArtifactRoute {
	t.Helper()
	var routes []catalogArtifactRoute
	readCatalogArtifact(t, catalogRoutesArtifact, &routes)
	if len(routes) == 0 {
		t.Fatalf("%s 为空", catalogRoutesArtifact)
	}
	return routes
}

func readCatalogArtifact(t *testing.T, path string, target any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("解析 %s 失败: %v", path, err)
	}
}

// routeIdentityKey 是一条路由的完整身份：方法 + 路径 + id + 契约版本。
func routeIdentityKey(id, contractVersion, method, path string) string {
	return method + " " + path + " " + id + " " + contractVersion
}

func coreRouteIdentityKeys(routes []CoreRoute) []string {
	keys := make([]string, 0, len(routes))
	for _, route := range routes {
		keys = append(keys, routeIdentityKey(route.ID, route.ContractVersion, route.Method, route.Path))
	}
	sort.Strings(keys)
	return keys
}

func catalogRouteIdentityKeys(routes []catalogArtifactRoute) []string {
	keys := make([]string, 0, len(routes))
	for _, route := range routes {
		keys = append(keys, routeIdentityKey(route.ID, route.ContractVersion, route.Method, route.Path))
	}
	sort.Strings(keys)
	return keys
}

func assertSameRouteIdentities(t *testing.T, leftName string, left []string, rightName string, right []string) {
	t.Helper()
	if len(left) != len(right) {
		t.Fatalf("路由身份条数不一致：%s = %d，%s = %d", leftName, len(left), rightName, len(right))
	}
	for index := range left {
		if left[index] != right[index] {
			t.Fatalf("%s 与 %s 的路由身份在第 %d 条分叉：%q vs %q", leftName, rightName, index, left[index], right[index])
		}
	}
}

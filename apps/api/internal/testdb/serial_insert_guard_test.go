package testdb

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// 静态守卫：测试代码不得「向 public 的序列主键表插显式 id 却不推进序列」。
//
// 它挡的是什么：`INSERT INTO users (id, …) VALUES (42, …)` 这类写法既不推进
// users_id_seq、也不清理行。同一分片的所有包共用一个测试库的 public schema，于是
// 下一个「不给 id」的插入会取到 42 并撞 users_pkey 23505——受害者是**别的包**，
// 2026-11-17 覆盖率门禁连红两次就是这个（缺陷属于插入方，不属于受害者）。
//
// 它挡不住什么（诚实写明，别把它当完备证明）：
//   - 跨文件间接调用：helper 在 A 文件、INSERT 在 B 文件时，按文件粒度看不到关联；
//   - `fmt.Sprintf` / 字符串拼接出来的 SQL：扫描器只认字面量形态的语句；
//   - 非 test 代码里的同类写法（本守卫只扫 `_test.go`）；
//   - 运行期才决定的表名（变量表名）；
//   - 显式 id 但值是 `nextval(...)` 的合法写法会被放行（那确实推进了序列）。
//
// 判定口径（宁可漏报，不要误报）：
//  1. 「哪些表是序列主键」只来自 `database/migrations/*.sql` 的 `CREATE TABLE`：
//     `id BIGSERIAL` / `id SERIAL` / `id BIGINT GENERATED … AS IDENTITY`。不按文件名
//     猜、不硬编码表名清单。
//  2. 以下四种视为通过：
//     a. VALUES 首项是 `DEFAULT`（走 nextval）或 `nextval(...)`（自己推进序列）；
//     b. 同文件出现 `search_path` / `CREATE SCHEMA`（私有 schema，public 不参与写）；
//     c. 同文件调用 `testdb.AlignSerial` / `testdb.InsertUserWithExplicitID`；
//     d. 同文件自己 `CREATE TABLE <该表>`（本地 DDL 覆盖 migrations 的类型，常见于
//     迁移测试）。
//  3. 其余命中一律红，并点名 `文件:行 + 表 + 建议改法`。
//
// 已知豁免（每条必须写理由，且被 liveness 断言盯着）：见 serialInsertExemptions。
func TestNoUnalignedExplicitIDInsertIntoPublicSerialTable(t *testing.T) {
	apiRoot := apiRootDir(t)
	serialTables := publicSerialPrimaryKeyTables(t, apiRoot)
	if len(serialTables) == 0 {
		t.Fatal("没有从 migrations 解析出任何序列主键表：解析器坏了，守卫会变成永远绿")
	}

	var violations []string
	for _, file := range testFiles(t, apiRoot) {
		src := file.content
		rel := file.rel
		if _, exempt := serialInsertExemptions[rel]; exempt {
			continue
		}
		// b/c：文件级豁免（私有 schema / 已经走对齐 helper）。
		//
		// 判定走**代码 token**，不扫原始文本：注释里出现 "search_path" 不算隔离。
		// 这一点是自证变异逼出来的——探针注释写了「没有 search_path」，纯文本判定
		// 会把它当成已隔离，守卫于是静默放行。
		mentions := codeMentions(t, rel, []byte(src))
		if mentions["search_path"] || mentions["CREATE SCHEMA"] ||
			mentions["AlignSerial"] || mentions["InsertUserWithExplicitID"] {
			continue
		}
		for _, hit := range explicitIDInserts(src, serialTables) {
			// d：本文件自己建了这张表 → migrations 的类型不适用于它。
			if ownTablePattern(hit.table).MatchString(src) {
				continue
			}
			if hit.value == "" || hit.value == "DEFAULT" || hit.value == "default" {
				continue
			}
			if strings.HasPrefix(hit.value, "nextval(") {
				continue
			}
			violations = append(violations, fmtViolation(rel, hit.line, hit.table, hit.value))
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("显式 id 插入 public 序列主键表却没有推进序列（会让同库其他包撞 23505）：\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestSerialInsertExemptionsAreLive 豁免清单不能变成垃圾桶：登记的文件必须存在、
// 必须写理由，且必须仍然真的命中（否则说明豁免已经过期）。
func TestSerialInsertExemptionsAreLive(t *testing.T) {
	if len(serialInsertExemptions) == 0 {
		return
	}
	apiRoot := apiRootDir(t)
	files := testFiles(t, apiRoot)
	byRel := make(map[string]sourceFile, len(files))
	for _, file := range files {
		byRel[file.rel] = file
	}
	var stale []string
	for rel, reason := range serialInsertExemptions {
		if strings.TrimSpace(reason) == "" {
			stale = append(stale, rel+"（缺理由）")
			continue
		}
		if _, ok := byRel[rel]; !ok {
			stale = append(stale, rel+"（文件已不存在）")
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("豁免清单已过期：%v", stale)
	}
}

// serialInsertExemptions 是逐文件豁免：文件级判据会误报的真实例外。
var serialInsertExemptions = map[string]string{
	"bootstrap/extension_lifecycle_cleanup_fk_order_landmine_test.go": "故意制造 landmine 的对照测试：它先消费一个序列值、再用显式 id 落一行，专门验证夹具在插入前会修复序列。插入本身就是被测缺陷，不能加对齐（加了这条测试就失去意义）。",

	// 以下六个文件本身不出现 search_path，但整条链路都跑在**包内共享 harness** 建的
	// 私有 schema 上（public 不参与写）。按文件粒度的扫描器看不到这层间接关系，
	// 所以逐条登记；理由里写死 harness 的 file:line，便于复核与 liveness。
	"app/Models/Conditions/conditions_object_identity_integration_test.go":                     "所有用例走 newFixture（conditions_postgres_integration_test.go:154），其 search_path 设在同文件 :186。",
	"app/Models/Extensions/legacy_plugin_runtime_publication_postgres_integration_test.go":     "走 newPluginRuntimePublicationPGFixture（plugin_runtime_publication_postgres_integration_test.go:288），search_path 设在同文件 :314/:379。",
	"app/Models/Extensions/plugin_runtime_publication_transition_postgres_integration_test.go": "同上：newPluginRuntimePublicationPGFixture 的私有 schema。",
	"app/Models/Extensions/plugin_runtime_artifact_repair_postgres_integration_test.go":        "同上：newPluginRuntimePublicationPGFixture 的私有 schema；且该 schema 里的 extension_versions 由 fixture 自建（id BIGINT PRIMARY KEY，非 serial），显式插 101/201 不触碰任何序列。",
	"app/Support/HostAPI/v2_command_actor_postgres_integration_test.go":                        "走 newPostgresCommandHarness（v2_command_postgres_integration_test.go:356），search_path 设在同文件 :379。",
	"app/Support/HostAPI/v2_command_domains_postgres_integration_test.go":                      "走 newPostgresDomainCommandHarness（v2_command_domains_postgres_harness_test.go:51），search_path 设在同文件 :74。",
	"app/Support/IdentityRegistry/postgres_legacy_adoption_integration_test.go":                "走 newIdentityRegistryStoreFixture（postgres_fixture_test.go:31），search_path 设在同文件 :58/:82。",
	"app/Support/IdentityRegistry/postgres_publication_integration_test.go":                    "同上：newIdentityRegistryStoreFixture 的私有 schema。",
}

type sourceFile struct {
	rel     string
	content string
}

type insertHit struct {
	table string
	line  int
	value string
}

// publicSerialPrimaryKeyTables 从 migrations 解析「public schema 里 id 是序列主键」的表。
func publicSerialPrimaryKeyTables(t *testing.T, apiRoot string) map[string]struct{} {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(apiRoot, "database", "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	out := map[string]struct{}{}
	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		src := string(raw)
		for _, table := range serialPrimaryKeyTablesIn(src) {
			out[table] = struct{}{}
		}
	}
	return out
}

var (
	createTablePattern = regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?(?:public\.)?([a-z_][a-z0-9_]*)\s*\(`)
	// id BIGSERIAL / id SERIAL / id BIGINT GENERATED … AS IDENTITY
	serialIDPattern = regexp.MustCompile(`(?i)^\s*id\s+(BIGSERIAL|SERIAL|BIGINT\s+GENERATED\b[^,]*AS\s+IDENTITY)`)
)

func serialPrimaryKeyTablesIn(src string) []string {
	var tables []string
	for _, match := range createTablePattern.FindAllStringSubmatchIndex(src, -1) {
		table := src[match[2]:match[3]]
		// 取括号内的列定义（按首个右括号收尾；migrations 的列定义里不出现嵌套括号）。
		bodyStart := match[1]
		bodyEnd := strings.Index(src[bodyStart:], "\n);")
		if bodyEnd < 0 {
			continue
		}
		body := src[bodyStart : bodyStart+bodyEnd]
		for _, line := range strings.Split(body, "\n") {
			if serialIDPattern.MatchString(line) {
				tables = append(tables, table)
				break
			}
		}
	}
	return tables
}

func testFiles(t *testing.T, apiRoot string) []sourceFile {
	t.Helper()
	var files []sourceFile
	err := filepath.WalkDir(apiRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(apiRoot, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, sourceFile{rel: filepath.ToSlash(rel), content: string(raw)})
		return nil
	})
	if err != nil {
		t.Fatalf("walk api root: %v", err)
	}
	return files
}

// codeMentions 收集文件里**代码**（字符串字面量 + 标识符）出现的标记词，忽略注释。
//
// 为什么不用 strings.Contains(src, …)：注释里提到 search_path 的测试会被误判成
// 「已隔离」，而它恰恰可能没隔离。守卫宁可漏报也不要误报，但更不该被一句注释骗过。
func codeMentions(t *testing.T, rel string, src []byte) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	mentions := map[string]bool{}
	add := func(text string) {
		for _, needle := range []string{"search_path", "CREATE SCHEMA", "AlignSerial", "InsertUserWithExplicitID"} {
			if strings.Contains(text, needle) {
				mentions[needle] = true
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BasicLit:
			if node.Kind == token.STRING {
				if value, unquoteErr := strconv.Unquote(node.Value); unquoteErr == nil {
					add(value)
				} else {
					add(node.Value)
				}
			}
		case *ast.Ident:
			add(node.Name)
		}
		return true
	})
	return mentions
}

// patternCache 缓存「按表名」的正则：主循环是 表数 × 文件数，逐次 MustCompile
// 会把一条静态守卫拖到分钟级（实测 82s → 1s 级）。
var patternCache = struct {
	own     sync.Map
	inserts sync.Map
}{}

func ownTablePattern(table string) *regexp.Regexp {
	if cached, ok := patternCache.own.Load(table); ok {
		return cached.(*regexp.Regexp)
	}
	pattern := regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?(?:[a-z_][a-z0-9_]*\.)?` + regexp.QuoteMeta(table) + `\s*\(`)
	patternCache.own.Store(table, pattern)
	return pattern
}

func insertIDPattern(table string) *regexp.Regexp {
	if cached, ok := patternCache.inserts.Load(table); ok {
		return cached.(*regexp.Regexp)
	}
	pattern := regexp.MustCompile(`(?i)INSERT INTO\s+(?:public\.)?` + regexp.QuoteMeta(table) + `\s*\(\s*id\s*,`)
	patternCache.inserts.Store(table, pattern)
	return pattern
}

// explicitIDInserts 一次扫完文件里所有「显式 id 插入」，只保留命中序列主键表的那些。
//
// 为什么不在外面套「表数 × 文件数」的双层循环：那样每个文件要被 40 个表名正则各扫
// 一遍，实测把一条静态守卫拖到 70s+；单次扫描是 O(文件) 的。
func explicitIDInserts(src string, serialTables map[string]struct{}) []insertHit {
	var hits []insertHit
	for _, match := range anyIDInsertPattern.FindAllStringSubmatchIndex(src, -1) {
		table := src[match[2]:match[3]]
		if _, ok := serialTables[table]; !ok {
			continue
		}
		line := strings.Count(src[:match[0]], "\n") + 1
		tail := src[match[1]:]
		if len(tail) > 400 {
			tail = tail[:400]
		}
		hits = append(hits, insertHit{table: table, line: line, value: firstValuesElement(tail)})
	}
	return hits
}

var anyIDInsertPattern = regexp.MustCompile(`(?i)INSERT INTO\s+(?:public\.)?([a-z_][a-z0-9_]*)\s*\(\s*id\s*,`)

var valuesPattern = regexp.MustCompile(`(?is)VALUES\s*\(?\s*([^,\n)]+)`)

func firstValuesElement(tail string) string {
	match := valuesPattern.FindStringSubmatch(tail)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func fmtViolation(rel string, line int, table, value string) string {
	return rel + ":" + itoa(line) + " | " + table + " | 显式 id=" + value +
		" → 改用 testdb.InsertUserWithExplicitID / testdb.AlignSerial，或把 id 改成 DEFAULT，或给该文件加私有 schema"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func apiRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

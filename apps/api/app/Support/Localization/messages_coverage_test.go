package localization

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// 对外 reason 键的翻译棘轮（F-6）。
//
// 判断口径（改口径必须改这里，别只改代码）：
//   - 只覆盖**对外契约**里的 code/reason 字符串：出现在 HTTP envelope 的
//     `message`/`data.reason` 上、或作为字段级提示键返回给客户端的那些键。
//   - 不含内部 `ErrXxx` Go error 文本（`errors.New`/`fmt.Errorf` 的文案）。它们只进
//     日志与事件，不进 envelope，翻译它们没有意义。
//   - 不含事件/审计/账本 reason（例如 `extension.delivery_*`、`extension.hook_*`）：
//     那些走版本化 hook 结果与管理面自由文本，不进对外 reason 契约。
//
// 发现来源（全部脚本化，不手抄）：
//  1. 任何 const 名字以 `Code`/`Message` 开头的字符串常量——仓库既有约定，
//     `Code*` 是 envelope reason，`Message*` 是字段级提示键；
//  2. 作为下列构造器 reason 实参出现的字面量或同包/唯一包常量：
//     `NewError`、`NewErrorWithFields`、`Abort`、`ErrorResponse`、
//     `ErrorResponseWithFields`、`ErrorResponseWithRetryAt`、`fiber.NewError`。
//
// 为什么需要这条棘轮：`locales_test.go` 的 `collectAPIErrorCodes` 用的是
// `^[a-z][a-z0-9]*(?:\.[a-z0-9_]+)+$`，首段不允许下划线，于是 `api_token.*`、
// `trust_levels.*`、`entity_meta.*`、`human_verification.*`、`rate_limit.*` 整族
// 对外 reason 在发现阶段就消失了——删掉这些键的文案，旧测试照样绿，非 Web 客户端
// 拿到的是裸键名。它也只认字面量实参，常量形式的 reason 同样漏网。
//
// 已知边界（写在这里，免得下一个人重新发现一遍）：
//   - F6-2 构造器识别**按方法名匹配、不校验包路径**：`fiber.NewError` 其实是靠
//     `calleeName` 取 `SelectorExpr.Sel` 命中 `NewError` 才进表的，任何包里同名但
//     语义不同的 `X.NewError(status, other)` 都会被当成 reason 构造器（假阳性方向）。
//     当前靠「实参必须命中 reason 键形状」把噪音挡住；要彻底消除得按 import 解析。
//   - F6-3 实参解析只覆盖 `BasicLit` / `Ident` / `SelectorExpr`（同包，或全仓同名
//     常量取值唯一时跨包）。`fmt.Sprintf`、字符串拼接、map 取值等**静默跳过**，
//     既不报错也不计数：这些写法产出的键目前不会被棘轮看守。
//   - F6-4 扫描根是 `apps/api`，不含 `extensions/`（插件是独立 module）。插件 reason
//     （如 `content_policy.hook_not_found`）走 HostAPI 版本化协议结果与管理面自由
//     文本，配套 message 由插件自己给，**不进核心 envelope 的 message/data.reason**，
//     因此不纳入本棘轮；若将来核心开始直接转发插件 reason，必须把扫描根扩到
//     extensions/ 并给这些键补文案。
//   - 自证变异（改完机制要重跑，否则棘轮可能只是看起来有用）：把任一处 reason 改成
//     **函数内局部 const**、改成**包级 var**、删掉一条已有文案、在豁免表里塞一条
//     已不存在的键——四种都必须红。
var reasonKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$`)

// reasonArgumentIndex 记录各构造器里 reason 是第几个实参。
var reasonArgumentIndex = map[string]int{
	"NewError":                 1,
	"NewErrorWithFields":       1,
	"Abort":                    1,
	"ErrorResponse":            2,
	"ErrorResponseWithFields":  2,
	"ErrorResponseWithRetryAt": 2,
}

// reasonKeyExemptions 是「刻意不翻译」的对外 reason 键，每条必须写理由。
//
// 目前为空：所有被发现的对外 reason 都有中英文案。将来若出现确实不该翻译的键
// （例如纯协议诊断码，客户端应当自行渲染），在这里登记并说明原因；
// TestExternalReasonKeyExemptionsAreLive 会盯着它别腐烂。
var reasonKeyExemptions = map[string]string{}

func TestExternalReasonKeysAreLocalized(t *testing.T) {
	apiRoot := localizationAPIRoot(t)
	keys := discoverReasonKeys(t, apiRoot)

	var missing []string
	for key, sites := range keys {
		if _, exempt := reasonKeyExemptions[key]; exempt {
			continue
		}
		for _, locale := range []string{"zh-CN", "en-US"} {
			if messageCatalogHas(locale, key) {
				continue
			}
			missing = append(missing, locale+":"+key+"  <- "+sites[0])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("对外 reason 键缺翻译（非 Web 客户端会拿到裸键名）：\n  %s", strings.Join(missing, "\n  "))
	}
}

// 中英键集必须严格相等：只补一边等于让另一边回退到 zh-CN 中文，客户端语言错了也看不出来。
func TestExternalReasonKeysHaveLocaleParity(t *testing.T) {
	zh := localeKeySet("zh-CN")
	en := localeKeySet("en-US")
	if len(zh) != len(en) {
		t.Fatalf("locale key count differs: zh-CN=%d en-US=%d", len(zh), len(en))
	}
	var onlyZH, onlyEN []string
	for key := range zh {
		if _, ok := en[key]; !ok {
			onlyZH = append(onlyZH, key)
		}
	}
	for key := range en {
		if _, ok := zh[key]; !ok {
			onlyEN = append(onlyEN, key)
		}
	}
	if len(onlyZH) > 0 || len(onlyEN) > 0 {
		sort.Strings(onlyZH)
		sort.Strings(onlyEN)
		t.Fatalf("locale key sets differ: only zh-CN=%v only en-US=%v", onlyZH, onlyEN)
	}
}

// 豁免清单不能变成垃圾桶：登记了但已经没人用的键必须删掉，否则它会掩盖真实回归。
func TestExternalReasonKeyExemptionsAreLive(t *testing.T) {
	if len(reasonKeyExemptions) == 0 {
		return
	}
	keys := discoverReasonKeys(t, localizationAPIRoot(t))
	var stale []string
	for key, reason := range reasonKeyExemptions {
		if strings.TrimSpace(reason) == "" {
			stale = append(stale, key+"（缺理由）")
			continue
		}
		if _, found := keys[key]; !found {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("豁免清单已过期：这些键不再是发现的对外 reason，请删除或更新：%v", stale)
	}
}

func localizationAPIRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "../../.."))
}

func messageCatalogHas(locale, key string) bool {
	if _, found := lookupMessage(trustedRuntimeMessages, locale, key); found {
		return true
	}
	_, found := messages[locale][key]
	return found
}

func localeKeySet(locale string) map[string]struct{} {
	out := map[string]struct{}{}
	for key := range messages[locale] {
		out[key] = struct{}{}
	}
	for key := range trustedRuntimeMessages[locale] {
		out[key] = struct{}{}
	}
	return out
}

// discoverReasonKeys 返回 对外 reason 键 -> 发现位置（文件:形态），位置用于报错点名。
func discoverReasonKeys(t *testing.T, apiRoot string) map[string][]string {
	t.Helper()

	type sourceFile struct {
		rel  string
		dir  string
		src  []byte
		file *ast.File
	}

	fset := token.NewFileSet()
	var files []sourceFile
	// 每个目录（= 一个包）的常量字面量表，用于把 reason 实参里的标识符解析成键值。
	packageConsts := map[string]map[string]string{}
	// 全仓同名常量的取值集合：只有「唯一取值」时才允许跨包解析，避免把
	// 不同包里同名的 `reason` 常量误当同一个键。
	globalConsts := map[string]map[string]struct{}{}

	err := filepath.WalkDir(apiRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(apiRoot, path)
		if relErr != nil {
			return relErr
		}
		if strings.Contains(filepath.ToSlash(rel), "Support/Localization") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		parsed, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return parseErr
		}
		dir := filepath.Dir(path)
		files = append(files, sourceFile{rel: filepath.ToSlash(rel), dir: dir, src: src, file: parsed})
		if packageConsts[dir] == nil {
			packageConsts[dir] = map[string]string{}
		}
		collectStringConsts(parsed, packageConsts[dir], globalConsts)
		return nil
	})
	if err != nil {
		t.Fatalf("walk api root: %v", err)
	}

	found := map[string]map[string]struct{}{}
	add := func(key, site string) {
		if key == "ok" || !reasonKeyPattern.MatchString(key) {
			return
		}
		if found[key] == nil {
			found[key] = map[string]struct{}{}
		}
		found[key][site] = struct{}{}
	}

	for _, f := range files {
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.GenDecl:
				if node.Tok != token.CONST {
					return true
				}
				for _, spec := range node.Specs {
					valueSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range valueSpec.Names {
						if i >= len(valueSpec.Values) {
							continue
						}
						literal, ok := valueSpec.Values[i].(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							continue
						}
						if !strings.HasPrefix(name.Name, "Code") && !strings.HasPrefix(name.Name, "Message") {
							continue
						}
						add(strings.Trim(literal.Value, `"`), f.rel+":const "+name.Name)
					}
				}
			case *ast.CallExpr:
				index, ok := reasonArgumentIndex[calleeName(node.Fun)]
				if !ok || len(node.Args) <= index {
					return true
				}
				key, ok := resolveReasonArgument(node.Args[index], packageConsts[f.dir], globalConsts)
				if !ok {
					return true
				}
				add(key, f.rel+":call "+calleeName(node.Fun))
			}
			return true
		})
	}

	out := make(map[string][]string, len(found))
	for key, sites := range found {
		list := make([]string, 0, len(sites))
		for site := range sites {
			list = append(list, site)
		}
		sort.Strings(list)
		out[key] = list
	}
	return out
}

// collectStringConsts 收集「名字 → 字符串字面量」的绑定，供 reason 实参解析。
//
// 覆盖面必须是整棵文件语法树，而不只是顶层声明、也不只是 const：
//   - 函数体内的局部 const/var：`reason := "x.y"` 这类写法同样会把键送进 envelope，
//     只扫顶层会漏（F6-1 实测：改成局部 const 后棘轮全绿）；
//   - 包级 var：`var reason = "x.y"` 与 const 在运行时等价（F6-1 实测：改成包级 var
//     后棘轮同样全绿）。
//
// 因此这里用 ast.Inspect 走全树，同时接受 token.CONST 与 token.VAR。
func collectStringConsts(file *ast.File, packageScope map[string]string, global map[string]map[string]struct{}) {
	ast.Inspect(file, func(n ast.Node) bool {
		genDecl, ok := n.(*ast.GenDecl)
		if !ok || (genDecl.Tok != token.CONST && genDecl.Tok != token.VAR) {
			return true
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range valueSpec.Names {
				if i >= len(valueSpec.Values) {
					continue
				}
				literal, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				value := strings.Trim(literal.Value, "`\"")
				packageScope[name.Name] = value
				if global[name.Name] == nil {
					global[name.Name] = map[string]struct{}{}
				}
				global[name.Name][value] = struct{}{}
			}
		}
		return true
	})
}

func resolveReasonArgument(expr ast.Expr, packageScope map[string]string, global map[string]map[string]struct{}) (string, bool) {
	switch arg := expr.(type) {
	case *ast.BasicLit:
		if arg.Kind != token.STRING {
			return "", false
		}
		return strings.Trim(arg.Value, `"`), true
	case *ast.Ident:
		value, ok := packageScope[arg.Name]
		return value, ok
	case *ast.SelectorExpr:
		// 跨包常量：只有全仓唯一取值时才认，避免同名常量串味。
		values, ok := global[arg.Sel.Name]
		if !ok || len(values) != 1 {
			return "", false
		}
		for value := range values {
			return value, true
		}
	}
	return "", false
}

func calleeName(fun ast.Expr) string {
	switch expr := fun.(type) {
	case *ast.Ident:
		return expr.Name
	case *ast.SelectorExpr:
		return expr.Sel.Name
	}
	return ""
}

#!/usr/bin/env bash
# M3.1 独立验收验证器：manifest `badgeConditions[]` + 能力 + 事实候选目录 + `factCandidates` DTO。
#
# 断言来源：knowledge/reports/2026-11-12-badge-m3-manifest-acceptance.md
# 规范：    knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md（含 Errata 2/3）
#
# 三种结果（与第一刀同口径）：ok / FAIL / pend。**不接** ./scripts/test.sh。
#
# 硬约定：
#   * FAIL 只留给「代码在、行为错」。缺文件/缺夹具/无会话 → pend。
#   * schema 断言读**文件原文**（不是 Go 内嵌副本）：内嵌副本与文件不一致正是要抓的漂移。
#   * 双侧校验（Go Validate / ValidateV3JSONSchema）逐类分别判，不用一条断言概括两条路径。
#
# 退出码：0 无 FAIL / 1 有 FAIL / 2 预检失败 / 4 --strict 下有 pend
#
# 环境变量：SFORUM_ACCEPT_API_URL（默认 http://127.0.0.1:8080/api/v1）、
#           SFORUM_SESSION_COOKIE（缺省时 HTTP 级断言 pend）、DATABASE_URL（不被本脚本使用：
#           DB 断言只走 ./scripts/test-api-isolated.sh）
#
# 用法：
#   ./scripts/verify-badge-m3-manifest.sh
#   ./scripts/verify-badge-m3-manifest.sh --no-tests
#   ./scripts/verify-badge-m3-manifest.sh --mutate [MUT-id]
#   ./scripts/verify-badge-m3-manifest.sh --strict
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
MAN_DIR="$API_DIR/app/Support/ExtensionManifest"
SCHEMA_FILE="$MAN_DIR/schemas/manifest-v3.schema.json"
FIXTURE="$ROOT_DIR/extensions/fixtures/plugins/sforum-query-surface-reference/sforum.extension.json"
TSV="$ROOT_DIR/scripts/verify-badge-m3-manifest-mutations.tsv"
STRICT=0
RUN_TESTS=1
MUTATE=0
MUTATE_ONLY=''

usage() { sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }
for arg in "$@"; do
  case "$arg" in
    --strict) STRICT=1 ;;
    --no-tests) RUN_TESTS=0 ;;
    --mutate) MUTATE=1 ;;
    MUT-*|M3M-*) MUTATE=1; MUTATE_ONLY="$arg" ;;
    --help|-h) usage; exit 0 ;;
    *) printf '未知参数：%s\n' "$arg" >&2; exit 2 ;;
  esac
done

work="$(mktemp -d "${TMPDIR:-/tmp}/badge-m3-accept.XXXXXX")"
probe_dir=''
cleanup() { [[ -n "$probe_dir" && -d "$probe_dir" ]] && rm -rf "$probe_dir"; rm -rf "$work"; }
trap cleanup EXIT

PASS_N=0; FAIL_N=0; PEND_N=0
FAIL_LINES=(); PEND_LINES=()
step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
pass() { PASS_N=$((PASS_N+1)); printf '  ok    %s\n' "$1"; }
fail() { FAIL_N=$((FAIL_N+1)); FAIL_LINES+=("$1${2:+ — $2}"); printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"; }
pend() { PEND_N=$((PEND_N+1)); PEND_LINES+=("$1${2:+ — $2}"); printf '  pend  %s%s\n' "$1" "${2:+（$2）}"; }

command -v node >/dev/null || { echo '需要 node' >&2; exit 2; }
command -v go >/dev/null || { echo '需要 go' >&2; exit 2; }
[[ -f "$SCHEMA_FILE" ]] || { echo "缺 $SCHEMA_FILE" >&2; exit 2; }

# ================================================================ A schema 原文
step 'A schema 原文（逐字符，不读内嵌副本）'
node - "$SCHEMA_FILE" >"$work/schema.out" <<'NODE'
const fs = require('fs')
const schema = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'))
const def = schema.$defs?.badgeConditions
const root = schema.properties?.badgeConditions
const out = {}
out.rootRef = root?.$ref ?? ''
out.rootType = JSON.stringify(root?.type ?? null)
out.defType = JSON.stringify(def?.type ?? null)
out.maxItems = String(def?.maxItems ?? '')
out.itemAdditional = String(def?.items?.additionalProperties ?? '')
out.itemProps = Object.keys(def?.items?.properties ?? {}).sort().join(',')
out.keyPattern = def?.items?.properties?.key?.pattern ?? ''
out.keyMin = String(def?.items?.properties?.key?.minLength ?? '')
out.keyMax = String(def?.items?.properties?.key?.maxLength ?? '')
out.compareMin = String(def?.items?.properties?.compare?.minItems ?? '')
out.compareMax = String(def?.items?.properties?.compare?.maxItems ?? '')
out.compareEnum = JSON.stringify(def?.items?.properties?.compare?.items?.enum ?? [])
out.variantsMax = String(def?.items?.properties?.variants?.maxItems ?? '')
out.variantValuePattern = def?.items?.properties?.variants?.items?.properties?.value?.pattern ?? ''
out.labelRequired = JSON.stringify(def?.items?.properties?.label?.required ?? [])
out.unitRequired = JSON.stringify(def?.items?.properties?.unit?.required ?? [])
out.hasValueKind = String('valueKind' in (def?.items?.properties ?? {}))
out.rootAdditional = String(schema.additionalProperties ?? '')
for (const [k, v] of Object.entries(out)) console.log(`${k}=${v}`)
NODE
g() { grep -m1 "^$1=" "$work/schema.out" | cut -d= -f2-; }
if [[ ! -s "$work/schema.out" ]]; then
  fail 'A schema 可解析' 'node 读 schema 失败'
else
  [[ "$(g keyPattern)" == '^[a-z0-9][a-z0-9._-]{1,120}$' ]] \
    && pass 'A-01 key pattern 逐字符 == ^[a-z0-9][a-z0-9._-]{1,120}$' \
    || fail 'A-01 key pattern' "实际：$(g keyPattern)"
  [[ "$(g hasValueKind)" == 'false' ]] && pass 'A-02 $defs.badgeConditions 无 valueKind' || fail 'A-02 无 valueKind' 'schema 里又出现了 valueKind'
  [[ "$(g itemProps)" == 'compare,contractVersion,key,label,unit,variants' ]] \
    && pass 'A-03 条目属性集合 == {key,contractVersion,label,unit,compare,variants}' \
    || fail 'A-03 条目属性集合' "实际：$(g itemProps)"
  [[ "$(g rootRef)" == '#/$defs/badgeConditions' && "$(g defType)" == '["array","null"]' && "$(g itemAdditional)" == 'false' ]] \
    && pass 'A-04 根 $ref + $defs nullable array + 条目 additionalProperties:false' \
    || fail 'A-04 根/条目结构' "ref=$(g rootRef) defType=$(g defType) itemAdditional=$(g itemAdditional)"
  [[ "$(g maxItems)" == '32' && "$(g variantsMax)" == '16' ]] \
    && pass 'A-05 上限 32 / variants 16' || fail 'A-05 上限' "32=$(g maxItems) 16=$(g variantsMax)"
  [[ "$(g keyMin)" == '2' && "$(g keyMax)" == '121' ]] && pass 'A-06 key 长度 2–121' || fail 'A-06 key 长度' "$(g keyMin)–$(g keyMax)"
  [[ "$(g compareMin)" == '1' && "$(g compareMax)" == '5' && "$(g compareEnum)" == '["gte","gt","lte","lt","eq"]' ]] \
    && pass 'A-07 compare minItems1/maxItems5/枚举五值（无 ne）' || fail 'A-07 compare' "min=$(g compareMin) max=$(g compareMax) enum=$(g compareEnum)"
  [[ "$(g labelRequired)" == '["zh-CN","en-US"]' && "$(g unitRequired)" == '["zh-CN","en-US"]' ]] \
    && pass 'A-08 label/unit 双语 required' || fail 'A-08 双语 required' "label=$(g labelRequired) unit=$(g unitRequired)"
  [[ "$(g variantValuePattern)" == '^[a-z0-9][a-z0-9._-]{0,63}$' ]] && pass 'A-09 variant value pattern 与 Go 常量一致' || fail 'A-09 variant pattern' "实际：$(g variantValuePattern)"
fi

# ================================================================ B/C 行为探针
step 'B/C 双侧校验 + 候选/冲突/variant 探针（独立程序，不用实现方测试函数）'
if [[ ! -f "$FIXTURE" ]]; then
  pend 'B/C 探针' "缺夹具 $FIXTURE"
else
  probe_dir="$(mktemp -d "$API_DIR/tmp-m3probe-XXXXXX")"
  cat >"$probe_dir/main.go" <<'GO'
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	em "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

func res(err error) string {
	if err == nil {
		return "pass"
	}
	return "fail"
}

type testCase struct {
	name   string
	goEdit func(*em.Manifest)
	jsEdit func(root, item map[string]any)
}

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	base := em.Manifest{}
	if err := json.Unmarshal(raw, &base); err != nil {
		panic(err)
	}
	key := base.ID + ".onboarding.tasks"
	valid := em.ManifestBadgeCondition{
		Key:             key,
		ContractVersion: key + "@1",
		Label:           map[string]string{"zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks"},
		Unit:            map[string]string{"zh-CN": "个", "en-US": "tasks"},
		Compare:         []string{"gte", "lte"},
		Variants: []em.ManifestBadgeConditionVariant{
			{Value: "tutorial", Label: map[string]string{"zh-CN": "教程", "en-US": "Tutorial"}},
			{Value: "profile", Label: map[string]string{"zh-CN": "资料", "en-US": "Profile"}},
		},
	}
	goNoVariants := valid
	goNoVariants.Key, goNoVariants.ContractVersion = base.ID+".verified_reports", base.ID+".verified_reports@1"
	goNoVariants.Compare, goNoVariants.Variants, goNoVariants.Unit = []string{"gte"}, nil, nil
	goSeventeen := valid
	goSeventeen.Variants = nil
	for i := 0; i < 17; i++ {
		goSeventeen.Variants = append(goSeventeen.Variants, em.ManifestBadgeConditionVariant{
			Value: fmt.Sprintf("v%d", i), Label: map[string]string{"zh-CN": "变体", "en-US": "variant"},
		})
	}
	twelve := valid
	twelve.Variants = nil

	rawBase := map[string]any{}
	_ = json.Unmarshal(raw, &rawBase)
	decl := map[string]any{
		"key": key, "contractVersion": key + "@1",
		"label":   map[string]any{"zh-CN": "完成新手任务数", "en-US": "Completed onboarding tasks"},
		"unit":    map[string]any{"zh-CN": "个", "en-US": "tasks"},
		"compare": []any{"gte", "lte"},
		"variants": []any{
			map[string]any{"value": "tutorial", "label": map[string]any{"zh-CN": "教程", "en-US": "Tutorial"}},
			map[string]any{"value": "profile", "label": map[string]any{"zh-CN": "资料", "en-US": "Profile"}},
		},
	}
	withDecl := func(mut func(root, item map[string]any)) map[string]any {
		clone, _ := json.Marshal(rawBase)
		out := map[string]any{}
		_ = json.Unmarshal(clone, &out)
		entry, _ := json.Marshal(decl)
		item := map[string]any{}
		_ = json.Unmarshal(entry, &item)
		if mut != nil {
			mut(out, item)
		}
		out["badgeConditions"] = []any{item}
		return out
	}
	seventeenJS := make([]any, 0, 17)
	for i := 0; i < 17; i++ {
		seventeenJS = append(seventeenJS, map[string]any{"value": fmt.Sprintf("v%d", i), "label": map[string]any{"zh-CN": "变体", "en-US": "variant"}})
	}

	cases := []testCase{
		{"valid", func(m *em.Manifest) { m.BadgeConditions = []em.ManifestBadgeCondition{valid} }, func(_, _ map[string]any) {}},
		{"no-variants", func(m *em.Manifest) { m.BadgeConditions = []em.ManifestBadgeCondition{goNoVariants} },
			func(_, _ map[string]any) {}},
		{"no-unit", func(m *em.Manifest) {
			d := valid
			d.Unit = nil
			m.BadgeConditions = []em.ManifestBadgeCondition{d}
		}, func(_, _ map[string]any) {}},
		{"no-backend-empty-entry", func(m *em.Manifest) {
			m.Backend = em.ManifestBackend{}
			m.BadgeConditions = []em.ManifestBadgeCondition{valid}
		}, func(root, _ map[string]any) {
			// Go 零值 struct 序列化后 backend 键仍在、entry 为空：schema 的 backend.entry minLength:1 会拒。
			root["backend"] = map[string]any{"digest": "", "entry": "", "hostApiVersion": "", "protocolVersion": 0, "rpc": ""}
		}},
		{"no-backend-key", func(m *em.Manifest) {
			m.Backend = em.ManifestBackend{}
			m.BadgeConditions = []em.ManifestBadgeCondition{valid}
		}, func(root, _ map[string]any) {
			// 边界：根 required 不含 backend，schema 单独跑会放过「整个键都没有」的清单。
			delete(root, "backend")
		}},
		{"non-plugin", func(m *em.Manifest) {
			m.Type = em.TypeTheme
			m.BadgeConditions = []em.ManifestBadgeCondition{valid}
		}, func(_, _ map[string]any) {}},
		{"no-prefix", func(m *em.Manifest) {
			d := valid
			d.Key, d.ContractVersion = "onboarding.tasks", "onboarding.tasks@1"
			m.BadgeConditions = []em.ManifestBadgeCondition{d}
		}, func(_, item map[string]any) { item["key"] = "onboarding.tasks"; item["contractVersion"] = "onboarding.tasks@1" }},
		{"drift", func(m *em.Manifest) {
			d := valid
			d.ContractVersion = base.ID + ".condition.tasks@1"
			m.BadgeConditions = []em.ManifestBadgeCondition{d}
		}, func(_, item map[string]any) { item["contractVersion"] = base.ID + ".condition.tasks@1" }},
		{"empty-compare", func(m *em.Manifest) {
			d := valid
			d.Compare = []string{}
			m.BadgeConditions = []em.ManifestBadgeCondition{d}
		}, func(_, item map[string]any) { item["compare"] = []any{} }},
		{"seventeen-variants", func(m *em.Manifest) { m.BadgeConditions = []em.ManifestBadgeCondition{goSeventeen} },
			func(_, item map[string]any) { item["variants"] = seventeenJS }},
		{"label-missing-en", func(m *em.Manifest) {
			d := valid
			d.Label = map[string]string{"zh-CN": "只有中文"}
			m.BadgeConditions = []em.ManifestBadgeCondition{d}
		}, func(_, item map[string]any) { item["label"] = map[string]any{"zh-CN": "只有中文"} }},
		{"twelve-variants", func(m *em.Manifest) { m.BadgeConditions = []em.ManifestBadgeCondition{twelve} }, func(_, _ map[string]any) {}},
	}
	for _, tc := range cases {
		m := base
		tc.goEdit(&m)
		goErr := em.Validate(m)
		jsBody, _ := json.Marshal(withDecl(tc.jsEdit))
		jsErr := em.ValidateV3JSONSchema(jsBody)
		goText := ""
		if goErr != nil {
			goText = strings.ReplaceAll(goErr.Error(), "\n", " ")
			if len(goText) > 160 {
				goText = goText[:160]
			}
		}
		fmt.Printf("case.%s go=%s js=%s goerr=%s\n", tc.name, res(goErr), res(jsErr), goText)
	}

	// 候选目录：无候选时 fact.count 隐藏，有候选时出现；其余内置条件两面都在。
	keysOf := func(defs []conditions.Definition) map[string]bool {
		out := make(map[string]bool, len(defs))
		for _, d := range defs {
			out[d.Key] = true
		}
		return out
	}
	registry := conditions.Core()
	none := keysOf(registry.PublishableDefinitions(nil))
	withOne := keysOf(registry.PublishableDefinitions([]conditions.FactCandidate{{Key: "acme.wiki.pages", LabelZh: "页面", LabelEn: "Pages"}}))
	diff := []string{}
	for k := range withOne {
		if !none[k] {
			diff = append(diff, k)
		}
	}
	for k := range none {
		if !withOne[k] {
			diff = append(diff, "-"+k)
		}
	}
	fmt.Printf("publishable.none=%d\npublishable.with=%d\ndiff=%v\n", len(none), len(withOne), diff)

	candidates, conflicts := conditions.MergeFactDeclarations([]conditions.FactDeclaration{
		{ExtensionID: "acme.wiki", Key: "acme.wiki.pages", LabelZh: "页面", LabelEn: "Pages"},
		{ExtensionID: "acme.wiki", Key: "fact.count", LabelZh: "撞内置", LabelEn: "Core"},
		{ExtensionID: "acme.wiki", Key: "other.pages", LabelZh: "越界", LabelEn: "Out"},
		{ExtensionID: "acme.wiki", Key: "acme.wiki.pages", LabelZh: "重复", LabelEn: "Dup"},
	})
	fmt.Printf("merge.candidates=%d\n", len(candidates))
	reasons := map[string]int{}
	for _, c := range conflicts {
		reasons[c.Reason]++
	}
	fmt.Printf("merge.conflicts=%s\n", mustJSON(reasons))

	variantCases := []struct {
		name      string
		cands     []conditions.FactCandidate
		factKey   string
		variant   string
		wantAllow bool
	}{
		{"declared", []conditions.FactCandidate{{Key: "a.b", Variants: []conditions.FactVariant{{Value: "tutorial"}}}}, "a.b", "tutorial", true},
		{"undeclared", []conditions.FactCandidate{{Key: "a.b", Variants: []conditions.FactVariant{{Value: "tutorial"}}}}, "a.b", "nope", false},
		{"no-candidates", nil, "a.b", "nope", true},
		{"empty-variant", []conditions.FactCandidate{{Key: "a.b", Variants: []conditions.FactVariant{{Value: "tutorial"}}}}, "a.b", "", true},
		{"no-variants", []conditions.FactCandidate{{Key: "a.b"}}, "a.b", "any", true},
	}
	for _, vc := range variantCases {
		got := conditions.VariantDeclared(vc.cands, vc.factKey, vc.variant)
		fmt.Printf("variant.%s=%v(want=%v)\n", vc.name, got, vc.wantAllow)
	}

	if def, ok := capabilities.Find(capabilities.BadgesFactWrite); ok {
		fmt.Printf("capability.found=true\ncapability.risk=%s\ncapability.zh=%s\ncapability.en=%s\ncapability.desc_len=%d\n",
			def.Risk, def.LabelZH, def.LabelEN, len(def.Description))
	} else {
		fmt.Printf("capability.found=false\n")
	}
}

func mustJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		return "err"
	}
	return string(out)
}
GO
  if ! ( cd "$API_DIR" && go run "./$(basename "$probe_dir")" "$FIXTURE" ) >"$work/probe.out" 2>&1; then
    fail 'B/C 探针编译或执行' "$(tail -3 "$work/probe.out" | tr '\n' ' ' | cut -c1-240)"
  else
    pv() { grep -m1 "^$1=" "$work/probe.out" | cut -d= -f2-; }
    # B：六类拒绝双侧（+ 合法三形态）
    expect() {
      local name="$1" wantGo="$2" wantJS="$3" wantErr="${4:-}" line
      line="$(grep -m1 "^case.$name " "$work/probe.out")"
      if [[ -z "$line" ]]; then fail "B-$name" '探针无输出'; return; fi
      local gotGo gotJS gotErr
      gotGo="$(printf '%s' "$line" | sed -E 's/.*go=([a-z]+).*/\1/')"
      gotJS="$(printf '%s' "$line" | sed -E 's/.*js=([a-z]+).*/\1/')"
      gotErr="$(printf '%s' "$line" | sed -E 's/.*goerr=//')"
      if [[ "$gotGo" != "$wantGo" || "$gotJS" != "$wantJS" ]]; then
        fail "B-$name" "期望 go=$wantGo js=$wantJS，实际 go=$gotGo js=$gotJS（err=$gotErr）"
      elif [[ -n "$wantErr" && "$gotErr" != *"$wantErr"* ]]; then
        # 只验 pass/fail 会把「被别的规则拒掉」当成这条规则的功劳（假绿）。
        fail "B-$name" "拒绝了，但不是因为 $wantErr（实际 err=$gotErr）"
      else
        pass "B-$name（go=$wantGo js=$wantJS${wantErr:+，goerr 含 $wantErr}）"
      fi
    }
    expect valid pass pass
    expect no-variants pass pass
    expect no-unit pass pass
    # 缺 backend：Go rule #2 必拒；schema 也必拒（第 4 条 allOf = 冻结 spec Errata 裁决：
    # badgeConditions 存在则 required backend 且 type=plugin/protocolVersion=2）。此处断言从 js=pass 改为 js=fail —— 收紧了，不是放宽。
    expect no-backend-empty-entry fail fail 'badgeConditions rule #2'
    # 同上：根 required 不含 backend 的边界已被 badgeConditions 的 if/then 覆盖，不再放过。
    expect no-backend-key fail fail 'badgeConditions rule #2'
    # 非插件：Go 必拒，但**拒因来自通用主题校验**（theme+backend 不被支持），
    # badgeConditions rule #1 因此不可达（报告 §5 GAP-1）。这里只断言「Go 拒绝」。
    expect non-plugin fail pass
    expect no-prefix fail pass 'badgeConditions'
    expect drift fail pass 'contractVersion drift'
    expect empty-compare fail fail 'badgeConditions rule #4'
    expect seventeen-variants fail fail 'badgeConditions rule #5'
    expect label-missing-en fail fail 'badgeConditions rule #6'
    # C：候选可见性 / 冲突 / variant / 能力
    [[ "$(pv diff)" == '[fact.count]' ]] && pass 'C-01 候选只影响 fact.count（其余内置条件恒在）' || fail 'C-01 候选差异' "diff=$(pv diff)"
    [[ "$(pv publishable.none)" -gt 0 && "$(pv publishable.with)" -gt "$(pv publishable.none)" ]] \
      && pass "C-02 内置目录非空且加候选后 +1（$(pv publishable.none) → $(pv publishable.with)）" \
      || fail 'C-02 目录基数' "none=$(pv publishable.none) with=$(pv publishable.with)"
    [[ "$(pv merge.candidates)" == '1' ]] && pass 'C-03 三类冲突只留 1 条合法候选' || fail 'C-03 冲突收口' "candidates=$(pv merge.candidates)"
    conf="$(pv merge.conflicts)"
    if [[ "$conf" == *'"core_condition_key":1'* && "$conf" == *'"namespace":1'* && "$conf" == *'"duplicate_key":1'* ]]; then
      pass 'C-04 冲突三类各 1 条（撞 Core / 越界 / 重复），不静默覆盖'
    else
      fail 'C-04 冲突分类' "conflicts=$conf"
    fi
    bad=''
    for v in declared no-candidates empty-variant no-variants; do
      [[ "$(pv variant.$v)" == 'true(want=true)' ]] || bad="$bad $v=$(pv variant.$v)"
    done
    [[ "$(pv variant.undeclared)" == 'false(want=false)' ]] || bad="$bad undeclared=$(pv variant.undeclared)"
    [[ -z "$bad" ]] && pass 'C-05 variant 三态 + 无变体/无候选放行' || fail 'C-05 variant 判定' "不符：$bad"
    if [[ "$(pv capability.found)" == 'true' && -n "$(pv capability.zh)" && -n "$(pv capability.en)" ]]; then
      pass "C-06 能力 badges.fact.write 在目录（risk=$(pv capability.risk)）"
    else
      fail 'C-06 能力目录' "found=$(pv capability.found)"
    fi
    # 人工判定留痕：label 必须自解释（不出现裸 fact/能力），这里只挡「明显不可读」
    if [[ "$(pv capability.zh)" == *fact* || "$(pv capability.zh)" == *能力* ]]; then
      fail 'C-07 能力文案自解释' "zh=$(pv capability.zh)"
    else
      pass "C-07 能力文案人工判定通过（zh=$(pv capability.zh) / en=$(pv capability.en)）"
    fi
  fi
fi

# ================================================================ D DB / 行为门
step 'D 隔离真库：卸载后 unknown 成对 + variant 三态'
if [[ $RUN_TESTS -eq 1 ]]; then
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'UndeclaredFactUnknown|DeclaredFactReadsCounters' ./app/Models/Badges/... ) >"$work/db.out" 2>&1; then
    ran=$(grep -cE '^=== RUN' "$work/db.out")
    [[ "$ran" -ge 2 ]] && pass "D-01 卸载 unknown 与声明成立成对执行（RUN=$ran）" || fail 'D-01 unknown 成对' "只跑了 $ran 条"
  else
    fail 'D-01 unknown 成对' "$(grep -E '^(--- FAIL|FAIL)' "$work/db.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'ValidateBadgeRule(AcceptsDeclaredVariant|RejectsUndeclaredVariant|AllowsRuleWhenExtensionGone)' ./app/Models/Badges/... ) >"$work/variant.out" 2>&1; then
    ran=$(grep -cE '^=== RUN' "$work/variant.out")
    [[ "$ran" -ge 3 ]] && pass "D-02 variant 三态用例齐全（RUN=$ran）" || fail 'D-02 variant 三态' "只跑了 $ran 条（缺「卸载后仍可存」或「未声明值拒绝」）"
  else
    fail 'D-02 variant 三态' "$(grep -E '^(--- FAIL|FAIL)' "$work/variant.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -run 'BadgeCondition|FactCandidate|FactDeclaration|MergeFact|PublishableDefinitionsHides' ./app/Support/ExtensionManifest/... ./app/Models/Conditions/... ./app/Http/Controllers/Badges/... ) >"$work/unit.out" 2>&1; then
    pass 'D-03 manifest/候选/DTO 单测包 exit 0'
  else
    fail 'D-03 单测' "$(grep -E '^(--- FAIL|FAIL)' "$work/unit.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
else
  pend 'D 隔离真库' '--no-tests'
fi

# ================================================================ E 文档/门禁
step 'E 生成文档与门禁'
for f in "$ROOT_DIR/docs/extensions/catalogs/manifest-v3.md" "$ROOT_DIR/docs/extensions/catalogs/capabilities.md"; do
  [[ -f "$f" ]] || { fail "E 生成文档 $(basename "$f")" '缺失'; continue; }
done
if grep -q 'badgeConditions' "$ROOT_DIR/docs/extensions/catalogs/manifest-v3.md"; then
  grep -q 'valueKind' "$ROOT_DIR/docs/extensions/catalogs/manifest-v3.md" \
    && fail 'E-01 manifest-v3.md' '生成文档里仍出现 valueKind' \
    || pass 'E-01 manifest-v3.md 含 badgeConditions 且无 valueKind'
else
  fail 'E-01 manifest-v3.md' '未重生成（无 badgeConditions）'
fi
grep -q 'badges.fact.write' "$ROOT_DIR/docs/extensions/catalogs/capabilities.md" \
  && pass 'E-02 capabilities.md 含 badges.fact.write' || fail 'E-02 capabilities.md' '未重生成'
if [[ $RUN_TESTS -eq 1 ]]; then
  for gate in 'node tests/validate-docs.mjs' 'node tests/validate-v3-p0-catalogs.mjs'; do
    if ( cd "$ROOT_DIR" && $gate ) >"$work/gate.out" 2>&1; then pass "E-03 门禁 ${gate#node }"
    else fail "E-03 门禁 ${gate#node }" "$(tail -2 "$work/gate.out" | tr '\n' ' ' | cut -c1-200)"; fi
  done
else
  pend 'E-03 文档门禁' '--no-tests'
fi

# ================================================================ F HTTP 级（需会话）
step 'F HTTP 级（需管理员会话）'
if [[ -n "${SFORUM_SESSION_COOKIE:-}" ]]; then
  code=$(curl -sS -o "$work/conditions.json" -w '%{http_code}' -b "$SFORUM_SESSION_COOKIE" "${SFORUM_ACCEPT_API_URL:-http://127.0.0.1:8080/api/v1}/admin/badges/conditions" 2>/dev/null)
  if [[ "$code" == '429' ]]; then
    pend 'F-01 factCandidates 值级（HTTP）' 'dev 429 限流'
  elif [[ "$code" != '200' ]]; then
    fail 'F-01 factCandidates 值级（HTTP）' "code=$code"
  else
    node - "$work/conditions.json" >"$work/http.out" 2>&1 <<'NODE'
const payload = JSON.parse(require('fs').readFileSync(process.argv[2], 'utf8'))
// 端点走 apphttp.OK 信封 {code,message,data}：真实数据在 payload.data；无信封形态兼容直出。
const root = payload && typeof payload === 'object' && payload.data ? payload.data : payload
const items = root.items ?? []
const facts = root.factCandidates ?? []
const bad = []
// 合法键集合只有两种：全集，或去掉 omitempty 的 unit（unitZh/unitEn）。逐项断言，
// 不拿 items[0] 当全体代表——换排序就会误判。
const fullKeys = 'compare,describeEn,describeZh,key,labelEn,labelZh,params,unitEn,unitZh,valueKind'
const unitlessKeys = 'compare,describeEn,describeZh,key,labelEn,labelZh,params,valueKind'
let fullN = 0
let unitlessN = 0
for (const item of items) {
  const keys = Object.keys(item).sort().join(',')
  if (keys === fullKeys) fullN += 1
  else if (keys === unitlessKeys) unitlessN += 1
  else bad.push(`items 键漂移: ${keys}`)
}
if (!Array.isArray(root.factCandidates)) bad.push('factCandidates 不是数组')
if (facts.length === 0) bad.push('factCandidates 为空：F-01 判据要求至少 1 条候选（需已安装/启用声明了 badgeConditions 的扩展）')
for (const f of facts) {
  if (!f.key) bad.push('key 空')
  if (!f.label || !f.label.zh || !f.label.en) bad.push(`label 空值: ${f.key}`)
  if (!Array.isArray(f.compare)) bad.push(`compare 非数组: ${f.key}`)
  for (const v of f.variants ?? []) {
    if (!v.value) bad.push(`variant value 空: ${f.key}`)
    if (!v.label || !v.label.zh || !v.label.en) bad.push(`variant label 空: ${f.key}/${v.value}`)
  }
}
console.log(bad.length ? `bad=${bad.slice(0, 3).join('; ')}` : `items=${items.length} fullKeys=${fullN} unitless=${unitlessN} facts=${facts.length} clean`)
NODE
    out=$(cat "$work/http.out")
    [[ "$out" == clean* || "$out" == items=*clean ]] && pass "F-01 factCandidates 值级（$out）" || fail 'F-01 factCandidates 值级' "$out"
  fi
else
  pend 'F-01 factCandidates 值级（HTTP）' '无 SFORUM_SESSION_COOKIE'
fi

# ================================================================ M 变异
step 'M 变异自检'
run_mutation_cmd() {
  case "$1" in
    manifest) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Support/ExtensionManifest/... ) ;;
    conditions) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Models/Conditions/... ) ;;
    models) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Models/Badges/... ) ;;
    dto) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" ./app/Http/Controllers/Badges/... ) ;;
    static) ( cd "$ROOT_DIR" && ./scripts/verify-badge-m3-manifest.sh --no-tests ) ;;
    *) return 9 ;;
  esac
}
if [[ "$MUTATE" != 1 ]]; then
  pend 'MUT-* 变异自检' '未运行（加 --mutate 才执行）'
elif [[ ! -f "$TSV" ]]; then
  pend 'MUT-* 变异自检' "缺 $TSV"
else
  matched_mutation=0
  executed=0
  while IFS=$'\t' read -r -u 3 id file sed_expr cmd_id asserts note_txt; do
    [[ -z "$id" || "$id" == '#'* ]] && continue
    [[ -n "$MUTATE_ONLY" && "$id" != "$MUTATE_ONLY" ]] && continue
    executed=$((executed + 1))
    matched_mutation=1
    full="$ROOT_DIR/$file"
    [[ -f "$full" ]] || { pend "$id" "文件不存在：$file"; continue; }
    cp "$full" "$work/mut.bak"
    sed -i -E "$sed_expr" "$full"
    if cmp -s "$full" "$work/mut.bak"; then
      cp "$work/mut.bak" "$full"; fail "$id 锚点过期" "sed 无改动 —$note_txt"; continue
    fi
    if run_mutation_cmd "$cmd_id" </dev/null >"$work/mut.out" 2>&1; then
      cp "$work/mut.bak" "$full"; fail "$id 未变红" "变异后仍 exit 0（断言 $asserts 假绿）"
    else
      redline=$(grep -E '^[[:space:]]*(--- FAIL|FAIL|\(fail\))' "$work/mut.out" | head -2 | tr '\n' ' ' | cut -c1-200)
      cp "$work/mut.bak" "$full"
      cmp -s "$full" "$work/mut.bak" && pass "$id 变异后变红并已恢复（$asserts）— $redline" \
        || fail "$id 恢复失败" "$file 未回到原字节"
    fi
  done 3<"$TSV"
  # 防静默：指定了 id 却一行都没匹配到时必须记 FAIL，不许 exit 0 假装跑过。
  if [[ -n "$MUTATE_ONLY" && "$matched_mutation" -eq 0 ]]; then
    fail "$MUTATE_ONLY 不存在" "指定的变异 id 在 tsv 里不存在（无 mutation_id == $MUTATE_ONLY 的行）"
  fi
  # 覆盖自检：变异条数必须与 tsv 一致。少跑一条就是假绿——曾被内层 `docker run -i`
  # 抽干继承来的 stdin（= tsv）跳过 5 条，汇总里既无 ok 也无 FAIL。
  if [[ -n "$MUTATE_ONLY" ]]; then
    expected=$(awk -F'\t' -v id="$MUTATE_ONLY" '!/^[[:space:]]*#/ && NF>=6 && $1==id' "$TSV" | wc -l)
  else
    expected=$(awk -F'\t' '!/^[[:space:]]*#/ && NF>=6' "$TSV" | wc -l)
  fi
  if [[ "$executed" -eq "$expected" ]]; then
    pass "变异覆盖：$executed/$expected 条全部执行"
  elif [[ "$expected" -eq 0 ]]; then
    : # 指定 id 不存在：由上面的「不存在」FAIL 负责
  else
    fail '变异覆盖' "只执行 $executed / 应有 $expected（有变异被静默跳过）"
  fi
fi

step '汇总'
printf '  ok %d / FAIL %d / pend %d\n' "$PASS_N" "$FAIL_N" "$PEND_N"
[[ "$FAIL_N" -gt 0 ]] && { printf '\n  FAIL 清单：\n'; printf '    - %s\n' "${FAIL_LINES[@]}"; }
[[ "$PEND_N" -gt 0 ]] && { printf '\n  pend 清单：\n'; printf '    - %s\n' "${PEND_LINES[@]}"; }
[[ "$FAIL_N" -gt 0 ]] && exit 1
if [[ "$STRICT" == 1 && "$PEND_N" -gt 0 ]]; then printf '\n[exit 4] --strict：%d 条 pend。\n' "$PEND_N" >&2; exit 4; fi
exit 0

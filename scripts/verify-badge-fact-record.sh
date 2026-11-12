#!/usr/bin/env bash
# M3.2 独立验收验证器：Host 命令 `badges.fact.record`（四要素 / 输入解码 / 六个 reason /
# 审计分层 / factKey 归属 / 幂等 / delta·total / 能力目录）。
#
# 断言来源：knowledge/reports/2026-11-12-badge-fact-record-acceptance.md
# 规范：    knowledge/reports/2026-11-12-badge-conditions-manifest-spec.md（Errata 3）
#
# 三态：ok / FAIL / pend。**不接**门禁。
#
# 做法：契约/解码/拒绝/审计分层/归属用**临时测试文件**注入 `HostAPI` 包（package hostapi，
# 复用该包既有 fake），跑完即删——这些构造器是 unexported，外部 go run 探针够不到。
# 真库语义（幂等/clamp 存储侧/审计行）跑实现方既有隔离用例。
#
# 退出码：0 无 FAIL / 1 有 FAIL / 2 预检 / 4 --strict 有 pend
#
# 用法：
#   ./scripts/verify-badge-fact-record.sh            # 全量
#   ./scripts/verify-badge-fact-record.sh --no-tests # 只跑静态 + 契约探针
#   ./scripts/verify-badge-fact-record.sh --mutate [MUT-id]
#   ./scripts/verify-badge-fact-record.sh --strict
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"
HOSTAPI_DIR="$API_DIR/app/Support/HostAPI"
CTRL_DIR="$API_DIR/app/Http/Controllers/Badges"
TSV="$ROOT_DIR/scripts/verify-badge-fact-record-mutations.tsv"
STRICT=0; RUN_TESTS=1; MUTATE=0; MUTATE_ONLY=''

usage() { sed -n '2,24p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }
for arg in "$@"; do case "$arg" in
  --strict) STRICT=1 ;; --no-tests) RUN_TESTS=0 ;; --mutate) MUTATE=1 ;;
  MUT-*|M[0-9]*) MUTATE=1; MUTATE_ONLY="$arg" ;;
  --help|-h) usage; exit 0 ;;
  *) printf '未知参数：%s\n' "$arg" >&2; exit 2 ;;
esac; done

work="$(mktemp -d "${TMPDIR:-/tmp}/badge-fact-accept.XXXXXX")"
PROBE="$HOSTAPI_DIR/zz_m32_acceptance_test.go"
cleanup() { [[ -f "$PROBE" ]] && rm -f "$PROBE"; rm -rf "$work"; }
trap cleanup EXIT

PASS_N=0; FAIL_N=0; PEND_N=0; FAIL_LINES=(); PEND_LINES=()
step() { printf '\n== %s\n' "$*"; }
note() { printf '  ·    %s\n' "$*"; }
pass() { PASS_N=$((PASS_N+1)); printf '  ok    %s\n' "$1"; }
fail() { FAIL_N=$((FAIL_N+1)); FAIL_LINES+=("$1${2:+ — $2}"); printf '  FAIL  %s%s\n' "$1" "${2:+（$2）}"; }
pend() { PEND_N=$((PEND_N+1)); PEND_LINES+=("$1${2:+ — $2}"); printf '  pend  %s%s\n' "$1" "${2:+（$2）}"; }
has() { grep -q -- "$2" "$1" 2>/dev/null; }

command -v go >/dev/null || { echo '需要 go' >&2; exit 2; }
CMD="$HOSTAPI_DIR/v2_command_badges.go"
[[ -f "$CMD" ]] || { echo "缺 $CMD" >&2; exit 2; }

# ================================================================ A 契约四要素（静态）
step 'A 契约四要素（值级，不读注释）'
twelve() { grep -m 1 -E "$1" "$CMD" | sed -E 's/.*"([^"]+)".*/\1/'; }
[[ "$(twelve 'CommandBadgesFactRecordID *=')" == 'badges.fact.record' ]] \
  && pass 'A-01 命令 ID == badges.fact.record（不带 @N）' \
  || fail 'A-01 命令 ID' "$(twelve 'CommandBadgesFactRecordID *=')"
[[ "$(twelve 'CommandBadgesFactRecordVersion *=')" == '1' ]] && pass 'A-02 Version == "1"' || fail 'A-02 Version' "$(twelve 'CommandBadgesFactRecordVersion *=')"
[[ "$(twelve 'CommandBadgesFactRecordInputSchema *=')" == 'sforum.badges.fact.record.input' \
   && "$(twelve 'CommandBadgesFactRecordSchemaVersion *=')" == '1' ]] \
  && pass 'A-03 input schema ref == sforum.badges.fact.record.input@1（id 与 @1 分开存）' \
  || fail 'A-03 input schema ref' "$(twelve 'CommandBadgesFactRecordInputSchema *=')@$(twelve 'CommandBadgesFactRecordSchemaVersion *=')"
[[ "$(twelve 'CommandBadgesFactRecordOutputSchema *=')" == 'sforum.badges.fact.record.result' ]] \
  && pass 'A-04 result schema ref == sforum.badges.fact.record.result@1' \
  || fail 'A-04 result schema ref' "$(twelve 'CommandBadgesFactRecordOutputSchema *=')"
# A-06：ID 不得带 @（文档已明写「不存在 badges.fact.record@1 这样的命令 ID」）
if grep -rn '"badges\.fact\.record@1"' "$API_DIR/app" "$API_DIR/sdk" 2>/dev/null | grep -v '_test.go' | grep -v 'knowledge' | grep -q .; then
  fail 'A-06 ID 不带 @ 的 tripwire' '代码里出现了 "badges.fact.record@1" 字面量（应只在 PolicyId/docs）'
else
  pass 'A-06 代码里没有把 badges.fact.record@1 当命令 ID 的字面量'
fi
if grep -q 'sforum.badges.fact.record@1' "$ROOT_DIR/docs/extensions/badge-plugin-facts.md"; then
  pass 'A-07 文档声明 Policy ID = sforum.badges.fact.record@1（与 A-05 对照）'
else
  pend 'A-07 文档 Policy ID' 'docs/extensions/badge-plugin-facts.md 未声明 Policy ID'
fi

# ================================================================ B/C/E 契约探针（临时测试）
step 'B/C/E 契约探针（临时测试注入 HostAPI 包，跑完即删）'
cat >"$PROBE" <<'GO'
package hostapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

// 小审临时验收探针（M3.2）。复用本包既有 fake，跑完删除，不进仓库。

func zzDefinition(t *testing.T, recorder *fakeBadgeFactRecorder, decls ProtocolV2BadgeFactDeclarationReader, auditor *fakeBadgeFactAuditor) protocolV2CommandDefinition {
	t.Helper()
	return newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, decls, auditor)
}

func zzPrepare(t *testing.T, def protocolV2CommandDefinition, input map[string]any) (*protocolV2CommandPreparation, error) {
	t.Helper()
	return def.Prepare(context.Background(), nil, badgeFactRequest(t, input))
}

func zzReason(t *testing.T, err error) string {
	t.Helper()
	cmdErr, ok := err.(*protocolV2CommandError)
	if !ok {
		t.Fatalf("必须是协议错误，得到 %T %v", err, err)
	}
	return cmdErr.detail.GetReason()
}

// A-01..A-05 四要素（含 Policy）。
func TestZZM32ContractElements(t *testing.T) {
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 3}}
	def := zzDefinition(t, recorder, badgeFactDefinitions(), &fakeBadgeFactAuditor{})
	if def.ID != "badges.fact.record" {
		t.Errorf("A-01 ID = %q", def.ID)
	}
	if def.Version != "1" {
		t.Errorf("A-02 Version = %q", def.Version)
	}
	if def.InputSchemaID+"@"+def.InputSchemaVersion != "sforum.badges.fact.record.input@1" {
		t.Errorf("A-03 input ref = %q@%q", def.InputSchemaID, def.InputSchemaVersion)
	}
	if def.OutputSchemaID+"@"+def.OutputSchemaVersion != "sforum.badges.fact.record.result@1" {
		t.Errorf("A-04 result ref = %q@%q", def.OutputSchemaID, def.OutputSchemaVersion)
	}
	if def.ActorMode != protocolV2CommandActorService {
		t.Errorf("ActorMode = %q，want service", def.ActorMode)
	}
	if _, err := zzPrepare(t, def, validBadgeFactInput()); err != nil {
		t.Fatalf("合法请求必须通过：%v", err)
	}
}

// A-05：PolicyId 必须带 @1（docs 的 Policy ID 行 / 计划 §8.2「@1 只出现在 PolicyId 与 schema ref」）。
func TestZZM32PolicyID(t *testing.T) {
	def := zzDefinition(t, &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 1}}, badgeFactDefinitions(), &fakeBadgeFactAuditor{})
	prepared, err := zzPrepare(t, def, validBadgeFactInput())
	if err != nil {
		t.Fatalf("合法请求必须通过：%v", err)
	}
	found := false
	for _, decision := range prepared.Policy {
		if decision.GetPolicyId() == "sforum.badges.fact.record@1" &&
			decision.GetAllowed() &&
			decision.GetResourceId() == "acme.wiki.pages" &&
			decision.GetReason() != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("A-05 preparation 未携带 PolicyId=sforum.badges.fact.record@1（实际 %d 条 policy）", len(prepared.Policy))
	}
}

// B 输入解码：字段名逐字、十进制字符串、occurredAt RFC3339、trim。
func TestZZM32InputDecode(t *testing.T) {
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 1}}
	def := zzDefinition(t, recorder, badgeFactDefinitions(), &fakeBadgeFactAuditor{})
	input := map[string]any{
		"userId": "42", "factKey": "  acme.wiki.pages  ", "variant": " tutorial ",
		"delta": "-5", "occurredAt": "2026-11-12T10:00:00Z", "dedupeKey": " event-1 ",
	}
	prepared, err := zzPrepare(t, def, input)
	if err != nil {
		t.Fatalf("B 解码失败：%v", err)
	}
	if _, err := def.Execute(context.Background(), nil, nil, prepared); err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	got := recorder.inputs[0]
	if got.UserID != 42 {
		t.Errorf("B-01 userId 解码 = %d", got.UserID)
	}
	if got.Delta != -5 {
		t.Errorf("B-02 delta 解码 = %d（负数必须允许）", got.Delta)
	}
	if got.FactKey != "acme.wiki.pages" || got.Variant != "tutorial" || got.DedupeKey != "event-1" {
		t.Errorf("B-03 trim/透传 = %q/%q/%q", got.FactKey, got.Variant, got.DedupeKey)
	}
	if want := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC); !got.OccurredAt.Equal(want) {
		t.Errorf("B-04 occurredAt = %v，want %v", got.OccurredAt, want)
	}
	if got.SourceExtensionID != "acme.wiki" {
		t.Errorf("B-05 source 必须取运行时身份，得到 %q", got.SourceExtensionID)
	}
}

// C 六个 reason 逐字 + 拒绝不触达写口。
func TestZZM32Reasons(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"C-01 invalid_user_id", map[string]any{"userId": "0", "factKey": "acme.wiki.pages", "delta": "1"}, BadgeFactReasonInvalidUserID},
		{"C-02 invalid_delta", map[string]any{"userId": "7", "factKey": "acme.wiki.pages", "delta": "0"}, BadgeFactReasonInvalidDelta},
		{"C-03 invalid_occurred_at", map[string]any{"userId": "7", "factKey": "acme.wiki.pages", "delta": "1", "occurredAt": "不是时间"}, BadgeFactReasonInvalidOccurredAt},
		{"C-04 fact_key_not_declared", map[string]any{"userId": "7", "factKey": "acme.wiki.unlisted", "delta": "1"}, BadgeFactReasonKeyNotDeclared},
		{"C-05 fact_key_empty", map[string]any{"userId": "7", "factKey": "", "delta": "1"}, BadgeFactReasonKeyNotDeclared},
	}
	for _, tc := range cases {
		recorder := &fakeBadgeFactRecorder{}
		auditor := &fakeBadgeFactAuditor{}
		def := zzDefinition(t, recorder, badgeFactDefinitions(), auditor)
		_, err := zzPrepare(t, def, tc.in)
		if err == nil {
			t.Errorf("%s 必须拒绝", tc.name)
			continue
		}
		if reason := zzReason(t, err); reason != tc.want {
			t.Errorf("%s reason = %q，want %q", tc.name, reason, tc.want)
		}
		if len(auditor.events) != 1 || auditor.events[0].Reason != tc.want {
			t.Errorf("%s 审计必须恰好一条且 reason 相等，得到 %+v", tc.name, auditor.events)
		}
		if len(recorder.inputs) != 0 {
			t.Errorf("%s 被拒请求不得触达写口", tc.name)
		}
	}
	// C-06 fact_write_failed：写口报错 → reason 逐字 + 一条审计。
	recorder := &fakeBadgeFactRecorder{err: errors.New("db down")}
	auditor := &fakeBadgeFactAuditor{}
	def := zzDefinition(t, recorder, badgeFactDefinitions(), auditor)
	prepared, err := zzPrepare(t, def, validBadgeFactInput())
	if err != nil {
		t.Fatalf("prepare 不该失败：%v", err)
	}
	_, execErr := def.Execute(context.Background(), nil, nil, prepared)
	if execErr == nil {
		t.Fatalf("写口报错必须拒绝")
	}
	if reason := zzReason(t, execErr); reason != BadgeFactReasonWriteFailed {
		t.Errorf("C-06 reason = %q，want %q", reason, BadgeFactReasonWriteFailed)
	}
	if len(auditor.events) != 1 || auditor.events[0].Reason != BadgeFactReasonWriteFailed {
		t.Errorf("C-06 写失败必须留一条审计，得到 %+v", auditor.events)
	}
}

// C-07/C-08 审计分层：capability_denied 不出现；审计失败不改返回码。
func TestZZM32AuditLayering(t *testing.T) {
	// C-07：域层任何拒绝都不许产出 capability_denied 审计（那是共享闸门的 reason）。
	for _, broken := range []map[string]any{
		{"factKey": "acme.wiki.unlisted", "userId": "7", "delta": "1"},
		{"userId": "-1", "factKey": "acme.wiki.pages", "delta": "1"},
		{"userId": "7", "factKey": "acme.wiki.pages", "delta": "0"},
	} {
		auditor := &fakeBadgeFactAuditor{}
		def := zzDefinition(t, &fakeBadgeFactRecorder{}, badgeFactDefinitions(), auditor)
		_, _ = zzPrepare(t, def, broken)
		for _, event := range auditor.events {
			if event.Reason == BadgeFactReasonCapabilityDenied {
				t.Errorf("C-07 域层审计出现 capability_denied：%+v", event)
			}
		}
	}
	// C-08：审计写失败不得改变 reason，也不得放行。
	auditor := &fakeBadgeFactAuditor{err: errors.New("audit down")}
	def := zzDefinition(t, &fakeBadgeFactRecorder{}, badgeFactDefinitions(), auditor)
	_, err := zzPrepare(t, def, map[string]any{"factKey": "acme.wiki.unlisted", "userId": "7", "delta": "1"})
	if err == nil {
		t.Fatalf("C-08 审计失败不得放行")
	}
	if reason := zzReason(t, err); reason != BadgeFactReasonKeyNotDeclared {
		t.Errorf("C-08 reason = %q（审计失败不得改码）", reason)
	}
}

// E factKey 归属：必须按「调用方扩展自己的声明」判，不是全局集合。
func TestZZM32Ownership(t *testing.T) {
	decls := fakeBadgeFactDeclarations{keys: map[string][]string{
		"acme.wiki":    {"acme.wiki.pages"},
		"other.plugin": {"other.plugin.pages"},
	}}
	// E-01 自己声明的 key 通过。
	def := zzDefinition(t, &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 1}}, decls, &fakeBadgeFactAuditor{})
	if _, err := zzPrepare(t, def, map[string]any{"userId": "7", "factKey": "acme.wiki.pages", "delta": "1"}); err != nil {
		t.Errorf("E-01 自己声明的 key 必须通过：%v", err)
	}
	// E-02 未声明的 key 拒绝。
	auditor := &fakeBadgeFactAuditor{}
	def = zzDefinition(t, &fakeBadgeFactRecorder{}, decls, auditor)
	_, err := zzPrepare(t, def, map[string]any{"userId": "7", "factKey": "acme.wiki.unlisted", "delta": "1"})
	if err == nil || zzReason(t, err) != BadgeFactReasonKeyNotDeclared {
		t.Errorf("E-02 未声明 key 必须拒绝，得到 %v", err)
	}
	// E-03 关键：别的扩展声明了同一个 key，acme 冒用仍必须拒（证明按扩展分组，不是全局集合）。
	_, err = zzPrepare(t, def, map[string]any{"userId": "7", "factKey": "other.plugin.pages", "delta": "1"})
	if err == nil || zzReason(t, err) != BadgeFactReasonKeyNotDeclared {
		t.Errorf("E-03 冒用他人已声明的 key 必须拒绝，得到 %v", err)
	}
	// E-04 声明读口 nil → fail-closed。
	def = zzDefinition(t, &fakeBadgeFactRecorder{}, nil, &fakeBadgeFactAuditor{})
	_, err = zzPrepare(t, def, validBadgeFactInput())
	if err == nil || zzReason(t, err) != BadgeFactReasonKeyNotDeclared {
		t.Errorf("E-04 声明读口缺失必须 fail-closed，得到 %v", err)
	}
	// E-05 声明读口报错 → fact_write_failed（不是「未声明」）。
	def = zzDefinition(t, &fakeBadgeFactRecorder{}, fakeBadgeFactDeclarations{err: errors.New("decl down")}, &fakeBadgeFactAuditor{})
	_, err = zzPrepare(t, def, validBadgeFactInput())
	if err == nil || zzReason(t, err) != BadgeFactReasonWriteFailed {
		t.Errorf("E-05 声明读口报错必须是 fact_write_failed，得到 %v", err)
	}
}

// G-01 能力声明。
func TestZZM32Capability(t *testing.T) {
	def := zzDefinition(t, &fakeBadgeFactRecorder{}, badgeFactDefinitions(), &fakeBadgeFactAuditor{})
	if def.RequiredCapability != capabilities.BadgesFactWrite {
		t.Errorf("G-01 RequiredCapability = %q，want %q", def.RequiredCapability, capabilities.BadgesFactWrite)
	}
	if !strings.Contains(string(capabilities.BadgesFactWrite), "badges.fact.write") {
		t.Errorf("G-02 能力字面量漂移：%q", capabilities.BadgesFactWrite)
	}
}
GO

if ! ( cd "$API_DIR" && go vet ./app/Support/HostAPI/ >"$work/vet.out" 2>&1 ); then
  if grep -q 'zz_m32_acceptance_test.go' "$work/vet.out"; then
    fail 'B/C/E 探针编译' "$(grep -m2 'zz_m32' "$work/vet.out" | tr '\n' ' ' | cut -c1-240)"
  else
    note "go vet 报告与本探针无关的问题（$(tail -2 "$work/vet.out" | tr '\n' ' ')），继续跑选择性用例"
  fi
fi

if ( cd "$API_DIR" && go test -count=1 -v -run 'TestZZM32' ./app/Support/HostAPI/ ) >"$work/probe.out" 2>&1; then
  okrun=1
else
  okrun=0
fi
# 逐条映射（每个 test 一个或一组断言）
map_test() {
  local test_name="$1"; shift
  local ids=("$@")
  if grep -q -- "--- PASS: $test_name" "$work/probe.out"; then
    pass "${ids[*]}（$test_name PASS）"
  else
    local detail
    detail=$(grep -E "^\s+zz_m32_acceptance_test\.go:[0-9]+:" "$work/probe.out" | head -2 | tr '\n' ' ' | cut -c1-240)
    fail "${ids[*]}" "${detail:-$test_name 未通过}"
  fi
}
map_test TestZZM32ContractElements A-01 A-02 A-03 A-04
map_test TestZZM32PolicyID A-05
map_test TestZZM32InputDecode B-01 B-02 B-03 B-04 B-05
map_test TestZZM32Reasons C-01 C-02 C-03 C-04 C-05 C-06
map_test TestZZM32AuditLayering C-07 C-08
map_test TestZZM32Ownership E-01 E-02 E-03 E-04 E-05
map_test TestZZM32Capability G-01 G-02

# ================================================================ D/F 真库
step 'D/F 隔离真库：审计行 / 幂等 / clamp 并排 / 精度 / 命令数'
if [[ $RUN_TESTS -eq 1 ]]; then
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'TestBadgeFactDeniedAuditLandsInAuditEvents' ./app/Support/HostAPI/ ) >"$work/audit.out" 2>&1; then
    pass 'D-01 真库审计行：action + metadata(reason/factKey/extensionId/userId 值级) exit 0'
  else
    fail 'D-01 真库审计行' "$(grep -E '^(--- FAIL|FAIL)' "$work/audit.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'TestRecordFact(DedupeIsIdempotent|ClampsTotalAtZero|KeepsInt64Precision)' ./app/Models/Badges/... ) >"$work/store.out" 2>&1; then
    ran=$(grep -cE '^=== RUN' "$work/store.out")
    [[ "$ran" -ge 3 ]] && pass "F-01/F-03/F-04 真库幂等 + clamp 并排 + 精度（RUN=$ran）" || fail 'F-01/F-03/F-04' "只跑了 $ran 条"
  else
    fail 'F-01/F-03/F-04' "$(grep -E '^(--- FAIL|FAIL)' "$work/store.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  # F-03 并排的第二面：回执 clamp（命令层）。只验存储侧会把「回执没 clamp」当正确。
  if ( cd "$API_DIR" && go test -count=1 -v -run 'TestBadgeFactRecordClampsNegativeTotal' ./app/Support/HostAPI/ ) >"$work/clamp.out" 2>&1; then
    pass 'F-03 回执 clamp：命令层 total=0（与存储层 -3 并排成立）'
  else
    fail 'F-03 回执 clamp' "$(grep -E '^(--- FAIL|FAIL)' "$work/clamp.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  # G-03：顶层码 → HTTP 403（不是 500）。值级：测试必须断言 StatusForbidden 与码字面量。
  if grep -q 'StatusForbidden' "$CTRL_DIR/fact_denial_test.go" 2>/dev/null \
     && ( cd "$API_DIR" && go test -count=1 -run 'TestMapBadgeErrorProjectsFactDenial' ./app/Http/Controllers/Badges/ ) >"$work/g03.out" 2>&1; then
    pass 'G-03 mapBadgeError → 403 + forum.badge_fact_write_denied（值级断言 + 测试 PASS）'
  else
    fail 'G-03 403 映射' "$(grep -E '^(--- FAIL|FAIL)' "$work/g03.out" 2>/dev/null | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  # F-02：无 dedupeKey 的两次必须都插入（当前无显式用例 → 如实 pend，执行期补临时用例需在 Models/Badges 包，避让后端并发）
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'TestRecordFactEmptyDedupeKeyAlwaysInserts' ./app/Models/Badges/... ) >"$work/f02.out" 2>&1; then
    if grep -q -- '--- PASS: TestRecordFactEmptyDedupeKeyAlwaysInserts' "$work/f02.out"; then
      pass 'F-02 空 dedupeKey 两次都插入（inserted 双 true + total 累加 + 行数 2）'
    else
      fail 'F-02' '用例未执行（tripwire 过期？）'
    fi
  else
    fail 'F-02 空 dedupeKey 双插入' "$(grep -E '^(--- FAIL|FAIL)' "$work/f02.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
  if ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -v -run 'TestPostgresProtocolV2CommandRuntimePublishesDomainCommands' ./app/Support/HostAPI/ ) >"$work/runtime.out" 2>&1; then
    pass 'E-06 命令目录 17 条（含 badges.fact.record）exit 0'
  else
    fail 'E-06 命令目录 17 条' "$(grep -E '^(--- FAIL|FAIL)' "$work/runtime.out" | head -2 | tr '\n' ' ' | cut -c1-200)"
  fi
else
  pend 'D/F 真库' '--no-tests'
fi

# ================================================================ M 变异
step 'M 变异自检'
run_mutation_cmd() {
  case "$1" in
    hostapi) ( cd "$API_DIR" && go test -count=1 -run 'TestZZM32' ./app/Support/HostAPI/ ) ;;
    http) ( cd "$API_DIR" && go test -count=1 -run 'TestMapBadgeErrorProjectsFactDenial' ./app/Http/Controllers/Badges/ ) ;;
    clamp) ( cd "$API_DIR" && go test -count=1 -run 'TestBadgeFactRecordClampsNegativeTotal' ./app/Support/HostAPI/ ) ;;
    audit) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -run 'TestBadgeFactDeniedAuditLandsInAuditEvents' ./app/Support/HostAPI/ ) ;;
    models) ( cd "$API_DIR" && "$ROOT_DIR/scripts/test-api-isolated.sh" -run 'TestRecordFact' ./app/Models/Badges/... ) ;;
    *) return 9 ;;
  esac
}
if [[ "$MUTATE" != 1 ]]; then
  pend 'MUT-* 变异自检' '未运行（加 --mutate；**不要**与正在改同一批文件的同事并行）'
elif [[ ! -f "$TSV" ]]; then
  pend 'MUT-* 变异自检' "缺 $TSV"
else
  executed=0
  while IFS=$'\t' read -r -u 3 id file sed_expr cmd_id asserts note_txt; do
    [[ -z "$id" || "$id" == '#'* ]] && continue
    [[ -n "$MUTATE_ONLY" && "$id" != "$MUTATE_ONLY" ]] && continue
    executed=$((executed + 1))
    full="$ROOT_DIR/$file"
    [[ -f "$full" ]] || { pend "$id" "文件不存在：$file"; continue; }
    cp "$full" "$work/mut.bak"
    sed -i -E "$sed_expr" "$full"
    if cmp -s "$full" "$work/mut.bak"; then cp "$work/mut.bak" "$full"; fail "$id 锚点过期" "sed 无改动 — $note_txt"; continue; fi
    if run_mutation_cmd "$cmd_id" </dev/null >"$work/mut.out" 2>&1; then
      cp "$work/mut.bak" "$full"; fail "$id 未变红" "变异后仍 exit 0（$asserts 假绿）"
    else
      redline=$(grep -E '^[[:space:]]*(--- FAIL|FAIL|\(fail\))' "$work/mut.out" | head -2 | tr '\n' ' ' | cut -c1-200)
      cp "$work/mut.bak" "$full"
      cmp -s "$full" "$work/mut.bak" && pass "$id 变异后变红并已恢复（$asserts）— $redline" || fail "$id 恢复失败" "$file 未回到原字节"
    fi
  done 3<"$TSV"
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
    fail '变异覆盖' "tsv 里没有可执行行（MUTATE_ONLY=${MUTATE_ONLY:-<空>}）；指定 id 不存在即失败"
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

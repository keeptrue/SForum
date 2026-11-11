package trustlevelscontroller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gofiber/fiber/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// 本文件钉的是 **控制器接线**，不是领域规则：`restoreDefaults` 必须
// 「先读 previous → RestoreDefaults(previous) → EncodeConfig → save」。
//
// 领域层（`Models/TrustLevels` 的 restore_defaults_test.go）已经证明 RestoreDefaults
// 会保留墓碑；但如果控制器哪天被改回 `recommendedConfig()`，那四条测试会全部继续绿——
// 死掉的是接线，不是规则。B2 当初正是这个形状，所以这里从 HTTP 入口打到写入串上。

// restoreWire 是「读完再写」的读面桩：写盘之后 getAdmin 会再读一次策略，
// 所以读桩必须能切换到刚写进去的那份值，否则响应断言的是旧策略（假绿）。
type restoreWire struct {
	previous   trustlevels.Policy
	readErr    error
	failReads  int // 前 N 次读失败（0 = 从不失败）
	readCount  int
	written    string
	writeCalls int
}

func (w *restoreWire) read(context.Context) (trustlevels.Policy, error) {
	w.readCount++
	if w.readCount <= w.failReads {
		return trustlevels.Policy{}, w.readErr
	}
	if w.written == "" {
		return w.previous, nil
	}
	return trustlevels.DecodeConfig(w.written)
}

func (w *restoreWire) write(_ context.Context, _ identity.Actor, value string) error {
	w.writeCalls++
	w.written = value
	return nil
}

// newRestoreApp 装配一个只有恢复默认可用权限的控制器，并注入读写桩。
func newRestoreApp(t *testing.T, wire *restoreWire) *fiber.App {
	t.Helper()
	controller := NewController(nil, nil, nil, nil).
		WithFeatureGate(func(context.Context) (bool, error) { return true, nil }).
		WithPolicyReader(wire.read).
		WithConfigWriter(wire.write).
		WithActorLoader(func(fiber.Ctx) (identity.Actor, error) {
			return activeActor(identity.PermissionAdminAccess, identity.PermissionTrustManage), nil
		})
	return newTestApp(t, controller)
}

// decodeWritten 把写入串解回 wire 形状，便于逐字段断言（而不是字符串包含）。
func decodeWritten(t *testing.T, written string) trustlevels.Config {
	t.Helper()
	if written == "" {
		t.Fatal("恢复默认没有写入任何值")
	}
	var config trustlevels.Config
	if err := json.Unmarshal([]byte(written), &config); err != nil {
		t.Fatalf("写入串不是合法配置 JSON：%v，%s", err, written)
	}
	return config
}

// TestRestoreDefaultsKeepsTombstonesOnWire 是 B2 在**控制器这一层**的回归靶子：
//
//  1. 写入串必须带 deletedTierIds，且同时保留「既有墓碑」与「本次被移除的自建档位」；
//  2. 写入串里的档位必须回到推荐默认（不再带 tier_<n>）；
//  3. 响应里的 nextTierSeq 不得回退——否则前端会提示「将创建 tier_6」，
//     而服务端已经永远不会再派生这个 id。
func TestRestoreDefaultsKeepsTombstonesOnWire(t *testing.T) {
	previous, err := trustlevels.DecodeConfig(
		`{"schemaVersion":2,"tiers":[` + tierConfigJSON("member") + `,` + tierConfigJSON("tier_9") + `],"deletedTierIds":["tier_6"]}`)
	if err != nil {
		t.Fatalf("构造前置策略失败：%v", err)
	}
	if got := previous.NextTierSeq(); got != 10 {
		t.Fatalf("前置条件失败：NextTierSeq = %d，期望 10", got)
	}

	wire := &restoreWire{previous: previous}
	app := newRestoreApp(t, wire)

	status, body := doRequest(t, app, fiber.MethodPost, "/api/v1/admin/trust-levels/restore-defaults", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d（期望 200），body=%s", status, body)
	}
	if wire.writeCalls != 1 {
		t.Fatalf("写入调用次数 = %d，期望 1", wire.writeCalls)
	}

	config := decodeWritten(t, wire.written)
	if len(config.DeletedTierIDs) != 2 || config.DeletedTierIDs[0] != "tier_6" || config.DeletedTierIDs[1] != "tier_9" {
		t.Fatalf("写入串的 deletedTierIds = %v，期望保序 [tier_6 tier_9]（既有墓碑 + 被移除的自建档）", config.DeletedTierIDs)
	}
	for _, tier := range config.Tiers {
		if tier.ID == "tier_9" {
			t.Fatal("恢复默认仍带着被移除的自建档位 tier_9")
		}
	}
	if len(config.Tiers) != 4 {
		t.Fatalf("恢复默认写入了 %d 档，期望推荐默认 4 档", len(config.Tiers))
	}

	// 响应必须反映**写进去之后**的策略（读桩已切到新值）。
	var payload struct {
		Data struct {
			Config       string   `json:"config"`
			NextTierSeq  int      `json:"nextTierSeq"`
			ColorChoices []string `json:"colorChoices"`
			Levels       []struct {
				Level int `json:"level"`
			} `json:"levels"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("响应不是预期 JSON：%v，body=%s", err, body)
	}
	if payload.Data.NextTierSeq != 10 {
		t.Fatalf("响应 nextTierSeq = %d，期望 10（墓碑 tier_9 计入，不得回退）", payload.Data.NextTierSeq)
	}
	if payload.Data.NextTierSeq < 7 {
		t.Fatalf("响应 nextTierSeq = %d，回退到了墓碑之前的号段", payload.Data.NextTierSeq)
	}
	if len(payload.Data.Levels) != 5 {
		t.Fatalf("响应 levels 长度 = %d，期望 TL0 + 4 档 = 5", len(payload.Data.Levels))
	}
	if len(payload.Data.ColorChoices) != 9 {
		t.Fatalf("响应 colorChoices 长度 = %d，期望 9（恢复默认的载荷与后台读面同形）", len(payload.Data.ColorChoices))
	}
	if payload.Data.Config != wire.written {
		t.Fatalf("响应 config 与写入值不一致：响应=%s 写入=%s", payload.Data.Config, wire.written)
	}
}

// TestRestoreDefaultsFailOpenWithoutTombstones 钉住「读不到 previous 时退回纯推荐值」。
//
// 这是有意的 fail-open：配置已经坏掉的站点更需要这个按钮能按下去。把它改成
// fail-closed（读失败就 500）会让唯一能救场的入口失效，所以这里同时断言
// 「200 + 写入的是纯推荐值 + 不凭空造墓碑」，改语义会立刻红。
func TestRestoreDefaultsFailOpenWithoutTombstones(t *testing.T) {
	wire := &restoreWire{
		previous:  trustlevels.Policy{},
		readErr:   errors.New("boom"),
		failReads: 1, // 只让「读 previous」那一次失败，之后的 getAdmin 读要成功
	}
	app := newRestoreApp(t, wire)

	status, body := doRequest(t, app, fiber.MethodPost, "/api/v1/admin/trust-levels/restore-defaults", "")
	if status != fiber.StatusOK {
		t.Fatalf("status = %d（期望 200：读失败必须 fail-open），body=%s", status, body)
	}
	if wire.writeCalls != 1 {
		t.Fatalf("写入调用次数 = %d，期望 1（读失败仍要写推荐值）", wire.writeCalls)
	}
	if wire.written == "" {
		t.Fatal("读失败后没有写入任何值")
	}

	recommended, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		t.Fatalf("EncodeConfig(DefaultPolicy): %v", err)
	}
	if wire.written != recommended {
		t.Fatalf("读失败时写入的不是纯推荐值：\n got=%s\nwant=%s", wire.written, recommended)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(wire.written), &raw); err != nil {
		t.Fatalf("写入串不是合法 JSON：%v，%s", err, wire.written)
	}
	if _, ok := raw["deletedTierIds"]; ok {
		t.Fatalf("读失败时不该凭空造墓碑：%s", wire.written)
	}
}

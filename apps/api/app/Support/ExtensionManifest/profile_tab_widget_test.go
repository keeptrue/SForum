package extensionmanifest

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// profileTabTestManifest 是一个只包含「公开 L2 组件 + 一个权限键声明」的最小清单。
// 校验按函数级进行（与 regionPlacement 的 l2Widget 测试同一策略），避免把整包
// L2 校验的依赖拖进来；完整链路另由 TestProfileTabWidgetContributionPassesFullValidation 覆盖。
func profileTabTestManifest() Manifest {
	return Manifest{
		ID: "demo.profile",
		Components: []ManifestComponent{{
			ID:              "demo.profile.stats",
			ContractVersion: "demo.profile.stats@1",
			Action:          ComponentActionAdd,
			L2Component:     "assets/stats.mjs",
			PropsSchema:     "schemas/stats-props.json",
		}},
		PermissionDefinitions: []ManifestPermissionDefinition{{
			Key:              "demo.profile.view_stats",
			ContractVersion:  "demo.profile.view_stats@1",
			Label:            LocalizedText{Default: "View stats"},
			Description:      LocalizedText{Default: "See profile statistics."},
			AssignmentPolicy: "host",
		}},
	}
}

func TestProfileTabContributionPayloadValidation(t *testing.T) {
	manifest := profileTabTestManifest()
	gatedComponent := profileTabTestManifest()
	gatedComponent.Components[0].Permission = "demo.profile.view_stats"

	cases := []struct {
		name     string
		manifest Manifest
		payload  string
		wantErr  bool
	}{
		{name: "widget references own public l2 component", payload: `{"type":"l2Widget","componentId":"demo.profile.stats"}`},
		{name: "widget component id is normalized", payload: `{"type":"l2Widget","componentId":" Demo.Profile.Stats "}`},
		{name: "widget with declared permission", payload: `{"type":"l2Widget","componentId":"demo.profile.stats","permission":"demo.profile.view_stats"}`},
		{name: "widget missing component id", payload: `{"type":"l2Widget"}`, wantErr: true},
		{name: "widget unknown component", payload: `{"type":"l2Widget","componentId":"demo.profile.missing"}`, wantErr: true},
		{name: "widget references permission gated component", manifest: gatedComponent, payload: `{"type":"l2Widget","componentId":"demo.profile.stats"}`, wantErr: true},
		{name: "widget with href", payload: `{"type":"l2Widget","componentId":"demo.profile.stats","href":"/tags"}`, wantErr: true},
		{name: "widget with path", payload: `{"type":"l2Widget","componentId":"demo.profile.stats","path":"/profile/stats"}`, wantErr: true},
		{name: "widget with method", payload: `{"type":"l2Widget","componentId":"demo.profile.stats","method":"GET"}`, wantErr: true},
		{name: "undeclared permission", payload: `{"type":"l2Widget","componentId":"demo.profile.stats","permission":"demo.profile.missing"}`, wantErr: true},
		{name: "legacy hostLink still valid", payload: `{"type":"hostLink","href":"/tags"}`},
		{name: "legacy hostLink with declared permission", payload: `{"type":"hostLink","href":"/tags","permission":"demo.profile.view_stats"}`},
		{name: "legacy hostLink with component id", payload: `{"type":"hostLink","href":"/tags","componentId":"demo.profile.stats"}`, wantErr: true},
		{name: "legacy extensionRoute still valid", payload: `{"type":"extensionRoute","method":"GET","path":"/profile/stats"}`},
		{name: "legacy extensionRoute with component id", payload: `{"type":"extensionRoute","method":"GET","path":"/profile/stats","componentId":"demo.profile.stats"}`, wantErr: true},
		{name: "unknown type", payload: `{"type":"iframe"}`, wantErr: true},
		{name: "empty payload", payload: ``, wantErr: true},
		{name: "malformed json", payload: `{"type":`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := manifest
			if tc.manifest.ID != "" {
				target = tc.manifest
			}
			err := validateProfileTabContributionPayload(target, json.RawMessage(tc.payload))
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidManifest) {
					t.Fatalf("expected ErrInvalidManifest, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// 完整清单校验：确认 profileSection 的 l2Widget 分支接在 Validate 主链上，
// 且引用本包已声明的公开 L2 组件时整包通过。
func TestProfileTabWidgetContributionPassesFullValidation(t *testing.T) {
	body, err := os.ReadFile("testdata/v3/l2-plugin.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	manifest.Contributions = []ManifestContribution{{
		Point:   PointForumProfileTabs,
		ID:      "fixture.l2.profile.card",
		Order:   10,
		Label:   map[string]string{"en-US": "Card"},
		Icon:    "i-lucide-user",
		Payload: json.RawMessage(`{"type":"l2Widget","componentId":"fixture.l2.component.card"}`),
	}}
	if err := Validate(manifest); err != nil {
		t.Fatalf("widget profile tab contribution should validate: %v", err)
	}

	// 同一份清单里引用一个从未声明的权限键必须整包拒绝。
	manifest.Contributions[0].Payload = json.RawMessage(`{"type":"l2Widget","componentId":"fixture.l2.component.card","permission":"fixture.l2.undeclared"}`)
	if err := Validate(manifest); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("undeclared permission must be rejected, got %v", err)
	}
}

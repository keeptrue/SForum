package plugin

import (
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

// 只读宿主目录快照，供脚手架、离线校验与文档生成（F4.1 / F4.2 共用）。

// EventDefinition 是宿主事件目录项。
type EventDefinition struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Description   string   `json:"description"`
	PayloadFields []string `json:"payloadFields,omitempty"`
	PatchFields   []string `json:"patchFields,omitempty"`
	TimeoutMS     int      `json:"timeoutMs"`
	FailurePolicy string   `json:"failurePolicy"`
}

// CapabilityDefinition 是宿主能力目录项。
type CapabilityDefinition struct {
	Key         string `json:"key"`
	Risk        string `json:"risk"`
	LabelZH     string `json:"labelZh"`
	LabelEN     string `json:"labelEn"`
	Description string `json:"description"`
}

// ContributionPoint 是宿主贡献点目录项。
type ContributionPoint struct {
	ID          string `json:"id"`
	Owner       string `json:"owner"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	PayloadType string `json:"payloadType"`
}

// ScheduleDefinition 是宿主核心周期任务目录项（插件不得私自 cron）。
type ScheduleDefinition struct {
	ID          string        `json:"id"`
	JobKind     string        `json:"jobKind"`
	Queue       string        `json:"queue"`
	Interval    time.Duration `json:"interval"`
	Owner       string        `json:"owner"`
	Enabled     bool          `json:"enabled"`
	Description string        `json:"description"`
}

// KnownProviderSlots 返回宿主已知的 provider 槽位名（含内置默认与邮件槽）。
func KnownProviderSlots() []string {
	// 与 ProviderRegistry 默认 + mail.provider 对齐；保持稳定顺序。
	return []string{
		extensionsruntime.MailProviderSlot,
		"search.provider",
		storage.ProviderSlot,
		"human_verification.provider",
		"auth.risk.provider",
		"editor.sanitizer.provider",
		"notification.channel.web_push",
	}
}

// EventCatalog 返回宿主事件目录副本。
func EventCatalog() []EventDefinition {
	src := appevents.Definitions()
	out := make([]EventDefinition, 0, len(src))
	for _, item := range src {
		out = append(out, EventDefinition{
			Name:          item.Name,
			Kind:          item.Kind,
			Description:   item.Description,
			PayloadFields: append([]string{}, item.PayloadFields...),
			PatchFields:   append([]string{}, item.PatchFields...),
			TimeoutMS:     item.TimeoutMS,
			FailurePolicy: item.FailurePolicy,
		})
	}
	return out
}

// CapabilityCatalog 返回宿主能力目录副本。
func CapabilityCatalog() []CapabilityDefinition {
	src := capabilities.Catalog()
	out := make([]CapabilityDefinition, 0, len(src))
	for _, item := range src {
		out = append(out, CapabilityDefinition{
			Key:         item.Key,
			Risk:        item.Risk,
			LabelZH:     item.LabelZH,
			LabelEN:     item.LabelEN,
			Description: item.Description,
		})
	}
	return out
}

// ContributionPoints 返回宿主贡献点目录副本。
func ContributionPoints() []ContributionPoint {
	src := extensionmanifest.ContributionPointDefinitions()
	out := make([]ContributionPoint, 0, len(src))
	for _, item := range src {
		out = append(out, ContributionPoint{
			ID:          item.ID,
			Owner:       item.Owner,
			Kind:        item.Kind,
			Description: item.Description,
			PayloadType: item.PayloadType,
		})
	}
	return out
}

// CoreSchedules 返回宿主内置 schedule 目录（无 River Constructor）。
func CoreSchedules() []ScheduleDefinition {
	src := supportjobs.CoreScheduleDefinitions()
	out := make([]ScheduleDefinition, 0, len(src))
	for _, item := range src {
		out = append(out, ScheduleDefinition{
			ID:          item.ID,
			JobKind:     item.JobKind,
			Queue:       item.Queue,
			Interval:    item.Interval,
			Owner:       item.Owner,
			Enabled:     item.Enabled,
			Description: item.Description,
		})
	}
	return out
}

// KnownEvent 判断事件名是否在宿主目录中。
func KnownEvent(name string) bool {
	return appevents.Known(name)
}

// KnownCapability 判断能力 key 是否在宿主目录中。
func KnownCapability(key string) bool {
	return capabilities.Known(key)
}

package extensions

import (
	"context"
	"errors"
	"fmt"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

var ErrProviderSlotInspectionUnavailable = errors.New("extensions: provider slot inspection is unavailable")

type ProviderSlotArtifactInspection struct {
	ExtensionID       string `json:"extensionId"`
	ExtensionVersion  string `json:"extensionVersion"`
	PackageDigest     string `json:"packageDigest"`
	RuntimeInstanceID string `json:"runtimeInstanceId"`
}

type ProviderSlotContractInspection struct {
	ID                       string                         `json:"id"`
	Slot                     string                         `json:"slot"`
	ContractVersion          string                         `json:"contractVersion"`
	RequestSchema            string                         `json:"requestSchema"`
	ResponseSchema           string                         `json:"responseSchema"`
	RequestSchemaDigest      string                         `json:"requestSchemaDigest,omitempty"`
	ResponseSchemaDigest     string                         `json:"responseSchemaDigest,omitempty"`
	Fallback                 string                         `json:"fallback"`
	TimeoutMS                int                            `json:"timeoutMs"`
	Artifact                 ProviderSlotArtifactInspection `json:"artifact"`
	ContractRuntimeAvailable bool                           `json:"contractRuntimeAvailable"`
}

type ProviderSlotCandidateInspection struct {
	ID           string                         `json:"id"`
	TargetID     string                         `json:"targetId"`
	Label        string                         `json:"label"`
	Handler      string                         `json:"handler"`
	Priority     int                            `json:"priority"`
	Rank         int                            `json:"rank"`
	Artifact     ProviderSlotArtifactInspection `json:"artifact"`
	Availability string                         `json:"availability"`
}

type ProviderSlotConflictInspection struct {
	Kind         string   `json:"kind"`
	Priority     int      `json:"priority"`
	CandidateIDs []string `json:"candidateIds"`
}

type ProviderSlotSelectionInspection struct {
	ContractID       string                         `json:"contractId"`
	ContractVersion  string                         `json:"contractVersion"`
	Slot             string                         `json:"slot"`
	ContractArtifact ProviderSlotArtifactInspection `json:"contractArtifact"`
	CandidateID      string                         `json:"candidateId"`
	ProviderArtifact ProviderSlotArtifactInspection `json:"providerArtifact"`
	SelectedByUserID int64                          `json:"selectedByUserId"`
	SelectionAuditID int64                          `json:"selectionAuditEventId"`
	Revision         int64                          `json:"revision"`
	SelectedAt       time.Time                      `json:"selectedAt"`
	UpdatedAt        time.Time                      `json:"updatedAt"`
}

type ProviderSlotInspectionItem struct {
	Contract             ProviderSlotContractInspection    `json:"contract"`
	Candidates           []ProviderSlotCandidateInspection `json:"candidates"`
	Conflicts            []ProviderSlotConflictInspection  `json:"conflicts"`
	SelectionStatus      string                            `json:"selectionStatus"`
	Selection            *ProviderSlotSelectionInspection  `json:"selection,omitempty"`
	Availability         string                            `json:"availability"`
	UnavailabilityReason string                            `json:"unavailabilityReason,omitempty"`
}

type ProviderSlotInspection struct {
	Revision uint64                       `json:"revision"`
	Slots    []ProviderSlotInspectionItem `json:"slots"`
}

type ProviderSlotInspectionSource interface {
	ProviderSlotInspection(context.Context) (ProviderSlotInspection, error)
}

func (s *CatalogService) InspectProviderSlots(ctx context.Context, actor identity.Actor) (ProviderSlotInspection, error) {
	if ctx == nil {
		return ProviderSlotInspection{}, ErrProviderSlotInspectionUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ProviderSlotInspection{}, err
	}
	if !canViewExtensions(actor) {
		return ProviderSlotInspection{}, identity.ErrPermissionDenied
	}
	source, ok := s.runtime.(ProviderSlotInspectionSource)
	if !ok || source == nil {
		return ProviderSlotInspection{}, ErrProviderSlotInspectionUnavailable
	}
	return source.ProviderSlotInspection(ctx)
}

// ProviderSlotSelected 报告扩展当前是否持有任一版本化 provider 槽位选择：
// 既可能是 provider 候选，也可能是 contract owner。自动更新在 legacy 重启前
// 用它避免静默重置运营选择。
func (s *PostgresStore) ProviderSlotSelected(ctx context.Context, extensionID string) (bool, error) {
	extensionID = normalizeID(extensionID)
	if extensionID == "" {
		return false, nil
	}
	var selected bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM extension_provider_slot_selections
			WHERE provider_extension_id = $1 OR contract_extension_id = $1
		)
	`, extensionID).Scan(&selected)
	if err != nil {
		return false, fmt.Errorf("check provider slot selection: %w", err)
	}
	return selected, nil
}

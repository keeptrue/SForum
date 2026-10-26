package identity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	ProfileOperationSectionsList  = "sections.list"
	ProfileOperationSectionRead   = "section.read"
	ProfileOperationSectionUpdate = "section.update"
	ProfileOperationAccountRead   = "account.read"
	ProfileOperationAccountUpdate = "account.update"
)

var (
	ErrProfileProviderInvalid     = errors.New("identity: profile provider input is invalid")
	ErrProfileProviderUnavailable = errors.New("identity: profile provider is unavailable")
	ErrProfileProviderNotFound    = errors.New("identity: profile provider was not found")
)

// ProfileSection 是 Host 合成后的资料分区视图。
type ProfileSection struct {
	ProviderID string         `json:"providerId"`
	SectionID  string         `json:"sectionId"`
	Title      string         `json:"title,omitempty"`
	Priority   int            `json:"priority"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// ProfileAccountView 是 Host 合成后的账户扩展视图。
type ProfileAccountView struct {
	ProviderID string         `json:"providerId"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// ProfileProviderSource 返回确定性排序后的活跃 profile 提供方。
type ProfileProviderSource interface {
	ProfileProviders(ctx context.Context) ([]identityregistry.ProviderContribution, error)
	ResolveProfileProvider(ctx context.Context, providerID string) (identityregistry.ProviderContribution, error)
}

// ProfileProviderInvoker 调用单个 exact profile 提供方操作。
type ProfileProviderInvoker interface {
	InvokeExact(
		ctx context.Context,
		provider identityregistry.ProviderContribution,
		operation string,
		actorUserID int64,
		input map[string]any,
		accept func(context.Context, map[string]any, func() error) error,
	) error
}

// ProfileProviderComposer 以确定性 priority/id 顺序合成活跃 profile 提供方。
// sections.list / section.read 使用 omit 失败策略；写操作与 account 读写 fail_closed。
type ProfileProviderComposer struct {
	source  ProfileProviderSource
	invoker ProfileProviderInvoker
}

func NewProfileProviderComposer(
	source ProfileProviderSource,
	invoker ProfileProviderInvoker,
) (*ProfileProviderComposer, error) {
	if source == nil || invoker == nil {
		return nil, ErrProfileProviderUnavailable
	}
	return &ProfileProviderComposer{source: source, invoker: invoker}, nil
}

// ListSections 合成全部活跃 profile 提供方的分区列表。
func (c *ProfileProviderComposer) ListSections(
	ctx context.Context,
	actorUserID, targetUserID int64,
) ([]ProfileSection, error) {
	if c == nil || c.source == nil || c.invoker == nil {
		return nil, ErrProfileProviderUnavailable
	}
	if actorUserID < 0 || targetUserID <= 0 {
		return nil, ErrProfileProviderInvalid
	}
	providers, err := c.source.ProfileProviders(ctx)
	if err != nil {
		return nil, ErrProfileProviderUnavailable
	}
	var sections []ProfileSection
	for _, provider := range providers {
		if !profileProviderHasOperation(provider, ProfileOperationSectionsList) {
			continue
		}
		listed, listErr := c.invokeSectionsList(ctx, provider, actorUserID, targetUserID)
		if listErr != nil {
			// sections.list 固定 omit：跳过失败提供方，不污染整页。
			if profileOperationFailurePolicy(provider, ProfileOperationSectionsList) == identityregistry.ProviderFailureOmit {
				continue
			}
			return nil, listErr
		}
		sections = append(sections, listed...)
	}
	return sections, nil
}

// ReadSection 读取单个提供方分区。
func (c *ProfileProviderComposer) ReadSection(
	ctx context.Context,
	providerID, sectionID string,
	actorUserID, targetUserID int64,
) (ProfileSection, error) {
	if c == nil || c.source == nil || c.invoker == nil {
		return ProfileSection{}, ErrProfileProviderUnavailable
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sectionID = strings.TrimSpace(sectionID)
	if providerID == "" || sectionID == "" || actorUserID < 0 || targetUserID <= 0 || len(sectionID) > 200 {
		return ProfileSection{}, ErrProfileProviderInvalid
	}
	provider, err := c.resolveProfileProvider(ctx, providerID, ProfileOperationSectionRead)
	if err != nil {
		return ProfileSection{}, err
	}
	var section ProfileSection
	err = c.invoker.InvokeExact(
		ctx, provider, ProfileOperationSectionRead, actorUserID,
		map[string]any{
			"sectionId":    sectionID,
			"targetUserId": targetUserID,
			"actorUserId":  actorUserID,
		},
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseProfileSection(provider.ID, provider.Priority, output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			section = parsed
			return nil
		},
	)
	if err != nil {
		if profileOperationFailurePolicy(provider, ProfileOperationSectionRead) == identityregistry.ProviderFailureOmit {
			return ProfileSection{}, ErrProfileProviderNotFound
		}
		return ProfileSection{}, mapProfileProviderInvokeError(err)
	}
	return section, nil
}

// UpdateSection 更新单个提供方分区（fail_closed）。
func (c *ProfileProviderComposer) UpdateSection(
	ctx context.Context,
	providerID, sectionID string,
	actorUserID, targetUserID int64,
	fields map[string]any,
) (ProfileSection, error) {
	if c == nil || c.source == nil || c.invoker == nil {
		return ProfileSection{}, ErrProfileProviderUnavailable
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sectionID = strings.TrimSpace(sectionID)
	if providerID == "" || sectionID == "" || actorUserID <= 0 || targetUserID <= 0 ||
		actorUserID != targetUserID || len(sectionID) > 200 || len(fields) == 0 || len(fields) > 50 {
		return ProfileSection{}, ErrProfileProviderInvalid
	}
	provider, err := c.resolveProfileProvider(ctx, providerID, ProfileOperationSectionUpdate)
	if err != nil {
		return ProfileSection{}, err
	}
	var section ProfileSection
	err = c.invoker.InvokeExact(
		ctx, provider, ProfileOperationSectionUpdate, actorUserID,
		map[string]any{
			"sectionId":    sectionID,
			"targetUserId": targetUserID,
			"actorUserId":  actorUserID,
			"fields":       fields,
		},
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseProfileSection(provider.ID, provider.Priority, output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			section = parsed
			return nil
		},
	)
	if err != nil {
		return ProfileSection{}, mapProfileProviderInvokeError(err)
	}
	return section, nil
}

// ReadAccount 读取账户扩展视图（fail_closed）。
func (c *ProfileProviderComposer) ReadAccount(
	ctx context.Context,
	providerID string,
	actorUserID, targetUserID int64,
) (ProfileAccountView, error) {
	if c == nil || c.source == nil || c.invoker == nil {
		return ProfileAccountView{}, ErrProfileProviderUnavailable
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" || actorUserID < 0 || targetUserID <= 0 {
		return ProfileAccountView{}, ErrProfileProviderInvalid
	}
	provider, err := c.resolveProfileProvider(ctx, providerID, ProfileOperationAccountRead)
	if err != nil {
		return ProfileAccountView{}, err
	}
	var view ProfileAccountView
	err = c.invoker.InvokeExact(
		ctx, provider, ProfileOperationAccountRead, actorUserID,
		map[string]any{"targetUserId": targetUserID, "actorUserId": actorUserID},
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseProfileAccount(provider.ID, output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			view = parsed
			return nil
		},
	)
	if err != nil {
		return ProfileAccountView{}, mapProfileProviderInvokeError(err)
	}
	return view, nil
}

// UpdateAccount 更新账户扩展视图（fail_closed）。
func (c *ProfileProviderComposer) UpdateAccount(
	ctx context.Context,
	providerID string,
	actorUserID, targetUserID int64,
	fields map[string]any,
) (ProfileAccountView, error) {
	if c == nil || c.source == nil || c.invoker == nil {
		return ProfileAccountView{}, ErrProfileProviderUnavailable
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" || actorUserID <= 0 || targetUserID <= 0 ||
		actorUserID != targetUserID || len(fields) == 0 || len(fields) > 50 {
		return ProfileAccountView{}, ErrProfileProviderInvalid
	}
	provider, err := c.resolveProfileProvider(ctx, providerID, ProfileOperationAccountUpdate)
	if err != nil {
		return ProfileAccountView{}, err
	}
	var view ProfileAccountView
	err = c.invoker.InvokeExact(
		ctx, provider, ProfileOperationAccountUpdate, actorUserID,
		map[string]any{
			"targetUserId": targetUserID,
			"actorUserId":  actorUserID,
			"fields":       fields,
		},
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseProfileAccount(provider.ID, output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			view = parsed
			return nil
		},
	)
	if err != nil {
		return ProfileAccountView{}, mapProfileProviderInvokeError(err)
	}
	return view, nil
}

func (c *ProfileProviderComposer) invokeSectionsList(
	ctx context.Context,
	provider identityregistry.ProviderContribution,
	actorUserID, targetUserID int64,
) ([]ProfileSection, error) {
	var sections []ProfileSection
	err := c.invoker.InvokeExact(
		ctx, provider, ProfileOperationSectionsList, actorUserID,
		map[string]any{"targetUserId": targetUserID, "actorUserId": actorUserID},
		func(_ context.Context, output map[string]any, fence func() error) error {
			parsed, parseErr := parseProfileSectionsList(provider.ID, provider.Priority, output)
			if parseErr != nil {
				return parseErr
			}
			if fence != nil {
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
			}
			sections = parsed
			return nil
		},
	)
	if err != nil {
		return nil, mapProfileProviderInvokeError(err)
	}
	return sections, nil
}

func (c *ProfileProviderComposer) resolveProfileProvider(
	ctx context.Context,
	providerID, operation string,
) (identityregistry.ProviderContribution, error) {
	provider, err := c.source.ResolveProfileProvider(ctx, providerID)
	if err != nil {
		if errors.Is(err, identityregistry.ErrNotFound) {
			return identityregistry.ProviderContribution{}, ErrProfileProviderNotFound
		}
		return identityregistry.ProviderContribution{}, ErrProfileProviderUnavailable
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindProfile {
		return identityregistry.ProviderContribution{}, ErrProfileProviderNotFound
	}
	if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
		return identityregistry.ProviderContribution{}, ErrProfileProviderUnavailable
	}
	if !profileProviderHasOperation(provider, operation) {
		return identityregistry.ProviderContribution{}, ErrProfileProviderUnavailable
	}
	return provider, nil
}

func profileProviderHasOperation(provider identityregistry.ProviderContribution, operation string) bool {
	for _, op := range provider.Operations {
		if strings.TrimSpace(op.Name) == operation {
			return true
		}
	}
	return false
}

func profileOperationFailurePolicy(provider identityregistry.ProviderContribution, operation string) string {
	for _, op := range provider.Operations {
		if strings.TrimSpace(op.Name) == operation {
			return strings.TrimSpace(op.FailurePolicy)
		}
	}
	return identityregistry.ProviderFailureFailClosed
}

func parseProfileSectionsList(providerID string, priority int, output map[string]any) ([]ProfileSection, error) {
	if output == nil {
		return nil, ErrProfileProviderUnavailable
	}
	rawSections, ok := output["sections"].([]any)
	if !ok {
		// 兼容单段返回。
		if sectionID := strings.TrimSpace(stringFromAuthOutput(output, "sectionId")); sectionID != "" {
			section, err := parseProfileSection(providerID, priority, output)
			if err != nil {
				return nil, err
			}
			return []ProfileSection{section}, nil
		}
		return nil, ErrProfileProviderUnavailable
	}
	if len(rawSections) > 50 {
		return nil, ErrProfileProviderUnavailable
	}
	sections := make([]ProfileSection, 0, len(rawSections))
	for _, raw := range rawSections {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, ErrProfileProviderUnavailable
		}
		section, err := parseProfileSection(providerID, priority, item)
		if err != nil {
			return nil, err
		}
		sections = append(sections, section)
	}
	return sections, nil
}

func parseProfileSection(providerID string, priority int, output map[string]any) (ProfileSection, error) {
	if output == nil {
		return ProfileSection{}, ErrProfileProviderUnavailable
	}
	sectionID := strings.TrimSpace(stringFromAuthOutput(output, "sectionId"))
	if sectionID == "" || len(sectionID) > 200 {
		return ProfileSection{}, ErrProfileProviderUnavailable
	}
	title := strings.TrimSpace(stringFromAuthOutput(output, "title"))
	if len(title) > 200 {
		return ProfileSection{}, ErrProfileProviderUnavailable
	}
	fields := map[string]any{}
	if raw, ok := output["fields"].(map[string]any); ok {
		if len(raw) > 50 {
			return ProfileSection{}, ErrProfileProviderUnavailable
		}
		for key, value := range raw {
			key = strings.TrimSpace(key)
			if key == "" || len(key) > 120 {
				return ProfileSection{}, ErrProfileProviderUnavailable
			}
			fields[key] = value
		}
	}
	return ProfileSection{
		ProviderID: providerID,
		SectionID:  sectionID,
		Title:      title,
		Priority:   priority,
		Fields:     fields,
	}, nil
}

func parseProfileAccount(providerID string, output map[string]any) (ProfileAccountView, error) {
	if output == nil {
		return ProfileAccountView{}, ErrProfileProviderUnavailable
	}
	fields := map[string]any{}
	if raw, ok := output["fields"].(map[string]any); ok {
		if len(raw) > 50 {
			return ProfileAccountView{}, ErrProfileProviderUnavailable
		}
		for key, value := range raw {
			key = strings.TrimSpace(key)
			if key == "" || len(key) > 120 {
				return ProfileAccountView{}, ErrProfileProviderUnavailable
			}
			fields[key] = value
		}
	}
	return ProfileAccountView{ProviderID: providerID, Fields: fields}, nil
}

func mapProfileProviderInvokeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrProfileProviderInvalid) ||
		errors.Is(err, ErrProfileProviderUnavailable) ||
		errors.Is(err, ErrProfileProviderNotFound) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrProfileProviderUnavailable, err)
}

// RegistryProfileProviderSource 从 Identity Registry 读取活跃 profile 提供方。
type RegistryProfileProviderSource struct {
	Registry *identityregistry.Registry
}

func (s RegistryProfileProviderSource) ProfileProviders(context.Context) ([]identityregistry.ProviderContribution, error) {
	if s.Registry == nil {
		return nil, ErrProfileProviderUnavailable
	}
	providers := s.Registry.Providers(identityregistry.ProviderKindProfile)
	out := make([]identityregistry.ProviderContribution, 0, len(providers))
	for _, provider := range providers {
		if len(provider.Operations) == 0 || provider.Artifact.Core || provider.Artifact.RuntimeInstanceID == "" {
			continue
		}
		out = append(out, provider)
	}
	// Registry 已按 priority/id 排序；再做一次稳定排序以防实现漂移。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s RegistryProfileProviderSource) ResolveProfileProvider(_ context.Context, providerID string) (identityregistry.ProviderContribution, error) {
	if s.Registry == nil {
		return identityregistry.ProviderContribution{}, ErrProfileProviderUnavailable
	}
	provider, err := s.Registry.ResolveProvider(providerID)
	if err != nil {
		return identityregistry.ProviderContribution{}, err
	}
	if strings.TrimSpace(provider.Kind) != identityregistry.ProviderKindProfile {
		return identityregistry.ProviderContribution{}, identityregistry.ErrNotFound
	}
	return provider, nil
}

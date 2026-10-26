package moderation

import (
	"context"
	"log/slog"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type Service struct {
	store           Store
	targetValidator TargetValidator
	settingsStore   SettingsStore
	workbenchStore  WorkbenchStore
	indexer         DecisionIndexer
	readModels      DecisionReadModelInvalidator
}

func NewServiceWithWorkbench(store Store, validator TargetValidator, settingsStore SettingsStore, workbenchStore WorkbenchStore) *Service {
	return &Service{store: store, targetValidator: validator, settingsStore: settingsStore, workbenchStore: workbenchStore}
}

type DecisionIndexer interface {
	EnqueueIndex(ctx context.Context, topicID int64) error
	EnqueueDelete(ctx context.Context, topicID int64) error
}

type DecisionReadModelInvalidator interface {
	InvalidateModerationPublication(ctx context.Context, topicID int64, targetType string)
}

func (s *Service) WithDecisionReadModelInvalidator(invalidator DecisionReadModelInvalidator) *Service {
	s.readModels = invalidator
	return s
}

func NewServiceWithWorkbenchIndexer(store Store, validator TargetValidator, settingsStore SettingsStore, workbenchStore WorkbenchStore, indexer DecisionIndexer) *Service {
	service := NewServiceWithWorkbench(store, validator, settingsStore, workbenchStore)
	service.indexer = indexer
	return service
}

func (s *Service) GetSettings(ctx context.Context, actor identity.Actor) (Settings, error) {
	if !actor.Can(identity.PermissionModerationManage) {
		return Settings{}, identity.ErrPermissionDenied
	}
	return s.settingsStore.GetSettings(ctx)
}

func (s *Service) UpdateSettings(ctx context.Context, actor identity.Actor, settings Settings) (Settings, error) {
	if !actor.Can(identity.PermissionModerationManage) {
		return Settings{}, identity.ErrPermissionDenied
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, err
	}
	return s.settingsStore.SaveSettings(ctx, settings, actor.ID)
}

func (s *Service) ResetSettings(ctx context.Context, actor identity.Actor) (Settings, error) {
	if !actor.Can(identity.PermissionModerationManage) {
		return Settings{}, identity.ErrPermissionDenied
	}
	return s.settingsStore.ResetSettings(ctx, RecommendedSettings(), actor.ID)
}

func (s *Service) QueueCounts(ctx context.Context, actor identity.Actor) (QueueCounts, error) {
	if !actor.Can(identity.PermissionModerationReview) {
		return QueueCounts{}, identity.ErrPermissionDenied
	}
	return s.workbenchStore.QueueCounts(ctx)
}

func (s *Service) ListPending(ctx context.Context, actor identity.Actor, input WorkbenchListInput) (PendingList, error) {
	if !actor.Can(identity.PermissionModerationReview) {
		return PendingList{}, identity.ErrPermissionDenied
	}
	input.Page, input.PerPage = normalizePage(input.Page, input.PerPage)
	list, err := s.workbenchStore.ListPending(ctx, input)
	if err != nil {
		return PendingList{}, err
	}
	// 无 view_ip 时剥离全文 IP，避免审核员默认看到敏感个人数据。
	if !actor.Can(identity.PermissionModerationViewIP) {
		for i := range list.Items {
			list.Items[i].IPAddress = ""
			list.Items[i].LastEditIP = ""
		}
	}
	return list, nil
}

func (s *Service) ListReportItems(ctx context.Context, actor identity.Actor, input WorkbenchListInput) (ReportItemList, error) {
	if !actor.Can(identity.PermissionModerationReview) {
		return ReportItemList{}, identity.ErrPermissionDenied
	}
	input.Page, input.PerPage = normalizePage(input.Page, input.PerPage)
	list, err := s.workbenchStore.ListReportItems(ctx, input)
	if err != nil {
		return ReportItemList{}, err
	}
	if !actor.Can(identity.PermissionModerationViewIP) {
		for i := range list.Items {
			list.Items[i].IPAddress = ""
			list.Items[i].LastEditIP = ""
		}
	}
	return list, nil
}

func (s *Service) ListDecisions(ctx context.Context, actor identity.Actor, input DecisionListInput, admin bool) (DecisionList, error) {
	permission := identity.PermissionModerationReview
	if admin {
		permission = identity.PermissionModerationManage
	}
	if !actor.Can(permission) {
		return DecisionList{}, identity.ErrPermissionDenied
	}
	input.Page, input.PerPage = normalizePage(input.Page, input.PerPage)
	return s.workbenchStore.ListDecisions(ctx, input)
}

func (s *Service) GetReviewContext(ctx context.Context, actor identity.Actor, input ReviewContextInput) (ReviewContext, error) {
	if !actor.Can(identity.PermissionModerationReview) {
		return ReviewContext{}, identity.ErrPermissionDenied
	}
	item, err := s.workbenchStore.GetReviewContext(ctx, input)
	if err != nil {
		return ReviewContext{}, err
	}
	if !actor.Can(identity.PermissionModerationViewIP) {
		item.IPAddress = ""
		item.LastEditIP = ""
	}
	return item, nil
}

func (s *Service) SubmitDecision(ctx context.Context, actor identity.Actor, input DecisionInput) (Decision, error) {
	if !actor.Can(identity.PermissionModerationReview) {
		return Decision{}, identity.ErrPermissionDenied
	}
	if err := validateDecision(&input); err != nil {
		return Decision{}, err
	}
	input.ReviewerUserID = actor.ID
	contextItem, err := s.workbenchStore.GetReviewContext(ctx, ReviewContextInput{
		Source: input.Source, TargetType: input.TargetType, TargetID: input.TargetID, ReportID: input.ReportID,
	})
	if err != nil {
		return Decision{}, err
	}
	decision, err := s.workbenchStore.SubmitDecision(ctx, input)
	if err != nil {
		return Decision{}, err
	}
	if input.Source == SourcePrePublish && input.Action == ActionApprove && s.readModels != nil {
		s.readModels.InvalidateModerationPublication(ctx, contextItem.TopicID, input.TargetType)
	}
	s.refreshSearchAfterDecision(ctx, input, contextItem)
	return decision, nil
}

func (s *Service) refreshSearchAfterDecision(ctx context.Context, input DecisionInput, contextItem ReviewContext) {
	if s.indexer == nil || contextItem.TopicID <= 0 {
		return
	}
	var err error
	if input.TargetType == TargetTypeTopic && (input.Action == ActionHideAndClose || input.Action == ActionDeleteAndClose) {
		err = s.indexer.EnqueueDelete(ctx, contextItem.TopicID)
	} else if input.Action == ActionApprove || input.Action == ActionHideAndClose || input.Action == ActionDeleteAndClose {
		err = s.indexer.EnqueueIndex(ctx, contextItem.TopicID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "moderation: refresh topic search derivative failed", "topicId", contextItem.TopicID, "err", err)
	}
}

func NewService(store Store, validator TargetValidator) *Service {
	return &Service{store: store, targetValidator: validator}
}

// CreateReport 由登录活跃用户发起举报。
func (s *Service) CreateReport(ctx context.Context, actor identity.Actor, input CreateReportInput) (Report, error) {
	if actor.ID <= 0 || actor.Status != identity.UserStatusActive {
		return Report{}, identity.ErrPermissionDenied
	}
	input.ReporterUserID = actor.ID
	if err := validateCreateReportInput(input); err != nil {
		return Report{}, err
	}
	// 校验被举报目标存在且可公开举报。
	if s.targetValidator != nil {
		reportable, err := s.isTargetReportable(ctx, input.TargetType, input.TargetID)
		if err != nil {
			return Report{}, err
		}
		if !reportable {
			return Report{}, ErrReportTargetInvalid
		}
	}
	report, err := s.store.CreateReport(ctx, input)
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

// ListReports 供审核员查看举报队列。
func (s *Service) ListReports(ctx context.Context, actor identity.Actor, input ReportListInput) (ReportList, error) {
	if !actor.Can(identity.PermissionModerationReportReview) {
		return ReportList{}, identity.ErrPermissionDenied
	}
	input.Page, input.PerPage = normalizePage(input.Page, input.PerPage)
	return s.store.ListReports(ctx, input)
}

// UpdateReport 更新举报状态与审核备注。
func (s *Service) UpdateReport(ctx context.Context, actor identity.Actor, input UpdateReportInput) (Report, error) {
	if !actor.Can(identity.PermissionModerationReportReview) {
		return Report{}, identity.ErrPermissionDenied
	}
	if err := validateUpdateReportInput(input); err != nil {
		return Report{}, err
	}
	input.ReviewerUserID = actor.ID
	return s.store.UpdateReport(ctx, input)
}

func (s *Service) isTargetReportable(ctx context.Context, targetType string, targetID int64) (bool, error) {
	switch targetType {
	case TargetTypeTopic:
		return s.targetValidator.IsReportableTopic(ctx, targetID)
	case TargetTypeComment:
		return s.targetValidator.IsReportableComment(ctx, targetID)
	default:
		return false, nil
	}
}

func validateCreateReportInput(input CreateReportInput) error {
	if input.TargetID <= 0 {
		return ErrReportInvalid
	}
	if !isValidTargetType(input.TargetType) {
		return ErrReportInvalid
	}
	if !isValidReasonCode(input.ReasonCode) {
		return ErrReportInvalid
	}
	input.Body = strings.TrimSpace(input.Body)
	if len(input.Body) > 2000 {
		return ErrReportInvalid
	}
	return nil
}

func validateUpdateReportInput(input UpdateReportInput) error {
	if input.ReportID <= 0 {
		return ErrReportInvalid
	}
	if !isValidStatus(input.Status) {
		return ErrReportInvalid
	}
	input.ReviewNote = strings.TrimSpace(input.ReviewNote)
	if len(input.ReviewNote) > 2000 {
		return ErrReportInvalid
	}
	return nil
}

func isValidTargetType(value string) bool {
	return value == TargetTypeTopic || value == TargetTypeComment
}

func isValidReasonCode(value string) bool {
	switch value {
	case ReasonSpam, ReasonAbuse, ReasonIllegal, ReasonOffTopic, ReasonOther:
		return true
	default:
		return false
	}
}

func isValidStatus(value string) bool {
	switch value {
	case StatusOpen, StatusReviewing, StatusResolved, StatusRejected:
		return true
	default:
		return false
	}
}

func normalizePage(page, perPage int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

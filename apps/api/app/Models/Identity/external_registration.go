package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

// ExternalAuthRegistrationResult 是外部注册（用户+默认角色+link 原子事务）结果。
type ExternalAuthRegistrationResult struct {
	User       CurrentUser
	ProviderID string
	LinkID     int64
}

// ExternalRegistrationPreparation contains editable provider hints only.
// It deliberately excludes subjects, digests, artifact identity and OAuth material.
type ExternalRegistrationPreparation struct {
	UsernameHint  string `json:"usernameHint"`
	EmailHint     string `json:"emailHint"`
	DisplayName   string `json:"displayName"`
	EmailVerified bool   `json:"emailVerified"`
}

// ExternalRegistrationInput 是外部注册时收集的本地必填字段。
type ExternalRegistrationInput struct {
	Username    string
	Email       string
	DisplayName string
	Locale      string
}

// ExternalRegistrationValidator 复用权威注册字段/策略校验（无密码、无更弱副本）。
type ExternalRegistrationValidator interface {
	ValidateExternalRegister(ctx context.Context, input ExternalRegistrationInput) error
}

// WithRegistrationValidator 注入权威外部注册校验器。
func (s *ExternalAuthService) WithRegistrationValidator(v ExternalRegistrationValidator) *ExternalAuthService {
	if s != nil {
		s.deps.ValidateRegistration = v
	}
	return s
}

// WithRegistrationPolicyTx 注入 CompleteRegistration 使用的权威 Options
// 事务读取器。事务外快速拒绝仍可保留，但账号创建必须依赖该读取器。
func (s *ExternalAuthService) WithRegistrationPolicyTx(fn func(context.Context, pgx.Tx) (bool, error)) *ExternalAuthService {
	if s != nil {
		s.deps.RegistrationEnabledTx = fn
	}
	return s
}

// CompleteRegistration 原子地创建用户 + 默认角色 + external link。
// 不创建密码凭据（外部账号）。零用户站点禁止外部注册，首用户必须用 Core 密码 bootstrap。
// 提交前再次校验激活、exact artifact 和注册策略；user/default role/link/audit 同事务写入。
func (s *ExternalAuthService) CompleteRegistration(ctx context.Context, assertion ExternalAuthAssertion, input ExternalRegistrationInput) (ExternalAuthRegistrationResult, error) {
	if assertion.Operation != ExternalAuthOperationRegistration {
		return ExternalAuthRegistrationResult{}, ErrExternalAuthOperationMismatch
	}
	sourceOperation := assertion.SourceOperation
	if sourceOperation == "" {
		sourceOperation = ExternalAuthOperationRegistration
	}
	if sourceOperation != ExternalAuthOperationLogin && sourceOperation != ExternalAuthOperationRegistration {
		return ExternalAuthRegistrationResult{}, ErrExternalAuthOperationMismatch
	}
	input = input.Normalized()
	if err := s.validateExternalRegistrationInput(ctx, input); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if err := s.RequireActivated(ctx, assertion.ProviderID, ExternalAuthOperationRegistration); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}

	contribution, err := s.providerContribution(assertion.ProviderID)
	if err != nil {
		return ExternalAuthRegistrationResult{}, ErrExternalAuthProviderUnavailable
	}
	if !assertion.MatchesLiveContribution(contribution) {
		return ExternalAuthRegistrationResult{}, ErrExternalAuthArtifactMismatch
	}
	if !authProviderHasOperation(contribution, AuthOperationRegistrationComplete) {
		return ExternalAuthRegistrationResult{}, ErrExternalAuthProviderUnavailable
	}
	if sourceOperation == ExternalAuthOperationLogin {
		if err := s.RequireActivated(ctx, assertion.ProviderID, ExternalAuthOperationLogin); err != nil {
			return ExternalAuthRegistrationResult{}, err
		}
		if !authProviderHasOperation(contribution, AuthOperationLoginComplete) {
			return ExternalAuthRegistrationResult{}, ErrExternalAuthProviderUnavailable
		}
	}

	digest, err := assertion.resolvedDigest()
	if err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if err := s.ensureExternalRegistrationPolicy(ctx); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}

	postgresLink, ok := s.deps.LinkStore.(*PostgresExternalIdentityLinkStore)
	if !ok || s.deps.Pool == nil {
		return ExternalAuthRegistrationResult{}, ErrExternalIdentityLinkStoreUnavailable
	}
	tx, err := s.deps.Pool.Begin(ctx)
	if err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	defer tx.Rollback(ctx)

	if err := s.ensureExternalRegistrationPolicyTx(ctx, tx); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if err := registrationConflictsTx(ctx, tx, input.Username, input.Email); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	current, err := createUserWithoutCredentialTx(ctx, tx, CreateUserInput{
		Username:      input.Username,
		Email:         input.Email,
		DisplayName:   input.DisplayName,
		Locale:        input.Locale,
		EmailVerified: assertion.EmailVerified,
	})
	if err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if err := assignDefaultRoleTx(ctx, tx, current.ID); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}

	mutation, err := postgresLink.LinkTx(ctx, tx, LinkExternalIdentityInput{
		UserID:                current.ID,
		Provider:              contribution,
		ProviderOperation:     AuthOperationRegistrationComplete,
		ProviderSubjectDigest: digest,
		ActorUserID:           0,
		IdempotencyKey:        "registration:" + assertion.CorrelationID,
	})
	if err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if _, err := insertExternalRegistrationAuditTx(ctx, tx, externalRegistrationAuditInput{
		UserID:           current.ID,
		ProviderID:       contribution.ID,
		OwnerExtensionID: contribution.Artifact.ExtensionID,
		CorrelationID:    assertion.CorrelationID,
	}); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExternalAuthRegistrationResult{}, err
	}

	s.emitUserRegistered(ctx, current, input.Email)
	current, err = s.loadCurrentUser(ctx, current.ID)
	if err != nil {
		return ExternalAuthRegistrationResult{}, err
	}
	return ExternalAuthRegistrationResult{
		User:       current,
		ProviderID: assertion.ProviderID,
		LinkID:     mutation.Link.ID,
	}, nil
}

// ensureExternalRegistrationPolicy 是事务外快速策略检查；权威结果仍以事务内读取为准。
func (s *ExternalAuthService) ensureExternalRegistrationPolicy(ctx context.Context) error {
	hasAny, err := s.anyUser(ctx)
	if err != nil {
		return err
	}
	if !hasAny {
		return ErrExternalAuthBootstrapRequired
	}
	enabled, err := s.registrationEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrRegistrationDisabled
	}
	return nil
}

// ensureExternalRegistrationPolicyTx 在创建 user 的同一事务内重新读取权威策略。
func (s *ExternalAuthService) ensureExternalRegistrationPolicyTx(ctx context.Context, tx pgx.Tx) error {
	var txCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&txCount); err != nil {
		return err
	}
	if txCount == 0 {
		return ErrExternalAuthBootstrapRequired
	}
	if s.deps.RegistrationEnabledTx == nil {
		return fmt.Errorf("transactional registration policy is unavailable")
	}
	enabled, err := s.deps.RegistrationEnabledTx(ctx, tx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrRegistrationDisabled
	}
	return nil
}

func (s *ExternalAuthService) anyUser(ctx context.Context) (bool, error) {
	if s.deps.AnyUserExists != nil {
		return s.deps.AnyUserExists(ctx)
	}
	if s.deps.Pool == nil {
		return false, fmt.Errorf("identity pool unavailable")
	}
	var count int
	err := s.deps.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count > 0, err
}

func (s *ExternalAuthService) registrationEnabled(ctx context.Context) (bool, error) {
	if s.deps.RegistrationEnabled != nil {
		return s.deps.RegistrationEnabled(ctx)
	}
	return true, nil
}

func (s *ExternalAuthService) validateExternalRegistrationInput(ctx context.Context, input ExternalRegistrationInput) error {
	if s.deps.ValidateRegistration != nil {
		return s.deps.ValidateRegistration.ValidateExternalRegister(ctx, input)
	}
	return validateRegistrationLocalInputMinimal(input)
}

// Normalized 返回 trim 后的字段，displayName 缺省回落到 username。
func (input ExternalRegistrationInput) Normalized() ExternalRegistrationInput {
	username := strings.TrimSpace(input.Username)
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = username
	}
	locale := strings.TrimSpace(input.Locale)
	if locale == "" {
		locale = "zh-CN"
	}
	return ExternalRegistrationInput{
		Username:    username,
		Email:       strings.TrimSpace(input.Email),
		DisplayName: displayName,
		Locale:      locale,
	}
}

func validateRegistrationLocalInputMinimal(input ExternalRegistrationInput) error {
	input = input.Normalized()
	if input.Username == "" {
		return ErrExternalRegistrationFieldUsername
	}
	if input.Email == "" {
		return ErrExternalRegistrationFieldEmail
	}
	return nil
}

func registrationConflictsTx(ctx context.Context, tx pgx.Tx, username, email string) error {
	var usernameTaken, emailTaken bool
	err := tx.QueryRow(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM users WHERE username_lower = lower($1)),
		  EXISTS (SELECT 1 FROM users WHERE email_lower = lower($2))
	`, username, email).Scan(&usernameTaken, &emailTaken)
	if err != nil {
		return err
	}
	if usernameTaken {
		return ErrExternalRegistrationFieldUsername
	}
	if emailTaken {
		return ErrExternalRegistrationFieldEmail
	}
	return nil
}

func (s *ExternalAuthService) emitUserRegistered(ctx context.Context, current CurrentUser, email string) {
	publisher := appevents.EnsurePublisher(s.deps.Events)
	publisher.Emit(ctx, appevents.Envelope{
		Name:          appevents.UserRegistered,
		Kind:          appevents.KindObserve,
		ActorUserID:   current.ID,
		ResourceType:  "user",
		ResourceID:    strconv.FormatInt(current.ID, 10),
		CorrelationID: appevents.NewID(),
		Payload: map[string]any{
			"userId":   current.ID,
			"username": current.Username,
			"email":    email,
			"locale":   current.Locale,
		},
		OccurredAt: time.Now().UTC(),
	})
}

type externalRegistrationAuditInput struct {
	UserID           int64
	ProviderID       string
	OwnerExtensionID string
	CorrelationID    string
}

// insertExternalRegistrationAuditTx 只记录公开绑定字段，不记录 subject、token 或 verifier。
func insertExternalRegistrationAuditTx(ctx context.Context, tx pgx.Tx, input externalRegistrationAuditInput) (int64, error) {
	if input.UserID <= 0 || strings.TrimSpace(input.ProviderID) == "" {
		return 0, fmt.Errorf("external registration audit input invalid")
	}
	metadata, err := json.Marshal(map[string]any{
		"providerId":       input.ProviderID,
		"ownerExtensionId": input.OwnerExtensionID,
		"correlationId":    input.CorrelationID,
	})
	if err != nil {
		return 0, fmt.Errorf("encode external registration audit: %w", err)
	}
	var auditID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, target_user_id, action, metadata)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING id
	`, input.UserID, input.UserID, AuditActionExternalRegister, metadata).Scan(&auditID); err != nil {
		return 0, fmt.Errorf("record external registration audit: %w", err)
	}
	return auditID, nil
}

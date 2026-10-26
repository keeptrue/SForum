package identity

import (
	"context"
	"database/sql"
	"fmt"

	store "github.com/keeptrue/sforum/apps/api/database/sqlc"
)

func (s *postgresTxStore) AnyUserExists(ctx context.Context) (bool, error) {
	return s.queries.AnyUserExists(ctx)
}

func (s *postgresTxStore) FindRegistrationConflicts(ctx context.Context, username string, email string) (RegistrationConflicts, error) {
	row, err := s.queries.FindRegistrationConflicts(ctx, store.FindRegistrationConflictsParams{
		Username: username,
		Email:    email,
	})
	if err != nil {
		return RegistrationConflicts{}, fmt.Errorf("find registration conflicts: %w", err)
	}
	return RegistrationConflicts{UsernameTaken: row.UsernameTaken, EmailTaken: row.EmailTaken}, nil
}

func (s *postgresTxStore) CreateUser(ctx context.Context, input CreateUserInput) (CurrentUser, error) {
	row, err := s.queries.CreateUser(ctx, store.CreateUserParams{
		Username:            input.Username,
		Email:               input.Email,
		DisplayName:         input.DisplayName,
		Locale:              input.Locale,
		IsInitialSuperAdmin: input.IsInitialSuperAdmin,
		EmailVerified:       input.EmailVerified,
	})
	if err != nil {
		if fields := uniqueRegistrationFields(err); len(fields) > 0 {
			return CurrentUser{}, NewRegisterInvalid(fields)
		}
		return CurrentUser{}, fmt.Errorf("create user: %w", err)
	}
	current := CurrentUser{
		ID:                  row.ID,
		Username:            row.Username,
		DisplayName:         row.DisplayName,
		Locale:              row.Locale,
		Status:              UserStatus(row.Status),
		IsInitialSuperAdmin: row.IsInitialSuperAdmin,
		EmailVerified:       row.EmailVerified,
	}
	current.Avatar = currentUserAvatar(ctx, s.avatarBuilder, current, input.Email, sql.NullInt64{}, sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{})
	return current, nil
}

func (s *postgresTxStore) CreateCredential(ctx context.Context, userID int64, passwordHash string) error {
	if err := s.queries.CreateUserCredential(ctx, store.CreateUserCredentialParams{
		UserID:       userID,
		PasswordHash: passwordHash,
	}); err != nil {
		return fmt.Errorf("create user credential: %w", err)
	}
	return nil
}

func (s *postgresTxStore) GetDefaultRole(ctx context.Context) (Role, error) {
	row, err := s.queries.GetDefaultRole(ctx)
	if err != nil {
		return Role{}, fmt.Errorf("get default role: %w", err)
	}
	return mapRole(row.ID, row.Key, row.Alias, row.Description, row.IsSystem, row.IsDefault, row.IsDeletable, row.IsEnabled), nil
}

func (s *postgresTxStore) GetRole(ctx context.Context, roleKey string) (Role, error) {
	row, err := s.queries.GetRoleByKey(ctx, roleKey)
	if err != nil {
		return Role{}, fmt.Errorf("get role: %w", err)
	}
	return mapRole(row.ID, row.Key, row.Alias, row.Description, row.IsSystem, row.IsDefault, row.IsDeletable, row.IsEnabled), nil
}

func (s *postgresTxStore) AssignRole(ctx context.Context, userID int64, roleID int64) error {
	if err := s.queries.AssignRoleToUser(ctx, store.AssignRoleToUserParams{
		UserID: userID,
		RoleID: roleID,
	}); err != nil {
		return fmt.Errorf("assign role to user: %w", err)
	}
	return nil
}

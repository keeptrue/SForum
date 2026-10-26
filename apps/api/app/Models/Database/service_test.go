package database

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestServiceRequiresDatabaseManagePermission(t *testing.T) {
	service := NewService(&fakeStore{})
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.ListTables(context.Background(), actor)

	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestServiceRejectsSystemSchema(t *testing.T) {
	service := NewService(&fakeStore{})

	_, err := service.Detail(context.Background(), databaseActor(), "pg_catalog", "pg_class")

	if !errors.Is(err, ErrInvalidTable) {
		t.Fatalf("expected invalid table for system schema, got %v", err)
	}
}

func TestRowsMaskSensitiveColumnsAndBuildRevealKey(t *testing.T) {
	store := &fakeStore{
		detail: TableDetail{
			Schema:     "public",
			Name:       "users",
			Kind:       "table",
			PrimaryKey: []string{"id"},
			Columns: []Column{
				{Name: "id", DataType: "bigint", IsPrimaryKey: true},
				{Name: "username", DataType: "text"},
				{Name: "password_hash", DataType: "text"},
			},
		},
		rows: []map[string]any{{
			"id":            int64(1),
			"username":      "admin",
			"password_hash": "argon2-secret",
		}},
	}
	service := NewService(store)

	result, err := service.Rows(context.Background(), databaseActor(), "public", "users", RowsInput{
		Page:           1,
		PerPage:        20,
		Sort:           "username",
		Direction:      "desc",
		FilterColumn:   "username",
		FilterOperator: "contains",
		FilterValue:    "adm",
	})
	if err != nil {
		t.Fatalf("Rows returned error: %v", err)
	}

	if store.lastRowsInput.Sort != "username" || store.lastRowsInput.Direction != "desc" {
		t.Fatalf("expected sanitized sort input, got %#v", store.lastRowsInput)
	}
	if len(result.Rows) != 1 || result.Rows[0].RowKey == "" {
		t.Fatalf("expected one row with reveal key, got %#v", result.Rows)
	}
	passwordCell := result.Rows[0].Values["password_hash"]
	if !passwordCell.Sensitive || !passwordCell.Masked || passwordCell.Value != SensitiveMask {
		t.Fatalf("expected masked sensitive password hash, got %#v", passwordCell)
	}
	if result.Rows[0].Values["username"].Value != "admin" {
		t.Fatalf("expected plain username value, got %#v", result.Rows[0].Values["username"])
	}
	if !result.Columns[2].IsSensitive {
		t.Fatalf("expected password_hash column marked sensitive: %#v", result.Columns[2])
	}
}

func TestRowsRejectUnknownSortOrFilterColumn(t *testing.T) {
	service := NewService(&fakeStore{detail: simpleTableDetail()})

	_, err := service.Rows(context.Background(), databaseActor(), "public", "users", RowsInput{Sort: "missing"})
	if !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("expected invalid sort column, got %v", err)
	}

	_, err = service.Rows(context.Background(), databaseActor(), "public", "users", RowsInput{FilterColumn: "missing", FilterOperator: "eq"})
	if !errors.Is(err, ErrInvalidColumn) {
		t.Fatalf("expected invalid filter column, got %v", err)
	}
}

func TestRevealReturnsSingleSensitiveCell(t *testing.T) {
	store := &fakeStore{
		detail:      simpleTableDetail(),
		rows:        []map[string]any{{"id": int64(1), "api_token": "secret-token"}},
		revealValue: "secret-token",
	}
	service := NewService(store)

	rows, err := service.Rows(context.Background(), databaseActor(), "public", "users", RowsInput{})
	if err != nil {
		t.Fatalf("Rows returned error: %v", err)
	}

	result, err := service.Reveal(context.Background(), databaseActor(), "public", "users", RevealInput{
		RowKey: rows.Rows[0].RowKey,
		Column: "api_token",
	})
	if err != nil {
		t.Fatalf("Reveal returned error: %v", err)
	}

	if result.Value != "secret-token" {
		t.Fatalf("expected revealed token, got %#v", result)
	}
	if store.lastRevealInput.RowKeyValues["id"] != "1" {
		t.Fatalf("expected decoded row key id=1, got %#v", store.lastRevealInput.RowKeyValues)
	}
}

func TestRevealRequiresPrimaryKey(t *testing.T) {
	service := NewService(&fakeStore{
		detail: TableDetail{
			Schema: "public",
			Name:   "logs",
			Kind:   "table",
			Columns: []Column{
				{Name: "message", DataType: "text"},
				{Name: "secret", DataType: "text"},
			},
		},
	})

	_, err := service.Reveal(context.Background(), databaseActor(), "public", "logs", RevealInput{
		RowKey: "missing",
		Column: "secret",
	})

	if !errors.Is(err, ErrRevealUnavailable) {
		t.Fatalf("expected reveal unavailable without primary key, got %v", err)
	}
}

func TestExportCSVKeepsSensitiveColumnsMasked(t *testing.T) {
	service := NewService(&fakeStore{
		detail: simpleTableDetail(),
		rows: []map[string]any{{
			"id":        int64(1),
			"api_token": "secret-token",
		}},
	})

	csv, err := service.ExportCSV(context.Background(), databaseActor(), "public", "users", RowsInput{})
	if err != nil {
		t.Fatalf("ExportCSV returned error: %v", err)
	}

	if string(csv) != "id,api_token\n1,••••••••\n" {
		t.Fatalf("unexpected csv output:\n%s", string(csv))
	}
}

func simpleTableDetail() TableDetail {
	return TableDetail{
		Schema:     "public",
		Name:       "users",
		Kind:       "table",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", DataType: "bigint", IsPrimaryKey: true},
			{Name: "api_token", DataType: "text"},
		},
	}
}

func databaseActor() identity.Actor {
	return identity.Actor{
		ID:          1,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionDatabaseManage: true},
	}
}

type fakeStore struct {
	tables          []TableSummary
	detail          TableDetail
	rows            []map[string]any
	revealValue     any
	lastRowsInput   RowsInput
	lastRevealInput RevealInput
}

func (s *fakeStore) ListTables(context.Context) ([]TableSummary, error) {
	return s.tables, nil
}

func (s *fakeStore) TableDetail(_ context.Context, _ TableRef) (TableDetail, error) {
	if s.detail.Schema == "" {
		return TableDetail{}, ErrTableNotFound
	}
	return s.detail, nil
}

func (s *fakeStore) TableRows(_ context.Context, _ TableRef, _ TableDetail, input RowsInput) ([]map[string]any, bool, error) {
	s.lastRowsInput = input
	return s.rows, false, nil
}

func (s *fakeStore) RevealCell(_ context.Context, _ TableRef, _ TableDetail, input RevealInput) (any, error) {
	s.lastRevealInput = input
	return s.revealValue, nil
}

package database

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	defaultPerPage = 50
	maxPerPage     = 100
	maxExportRows  = 5000
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) ListTables(ctx context.Context, actor identity.Actor) ([]TableSummary, error) {
	if !actor.Can(identity.PermissionDatabaseManage) {
		return nil, identity.ErrPermissionDenied
	}
	return s.store.ListTables(ctx)
}

func (s *Service) Detail(ctx context.Context, actor identity.Actor, schema string, table string) (TableDetail, error) {
	if !actor.Can(identity.PermissionDatabaseManage) {
		return TableDetail{}, identity.ErrPermissionDenied
	}

	ref, err := tableRef(schema, table)
	if err != nil {
		return TableDetail{}, err
	}

	detail, err := s.store.TableDetail(ctx, ref)
	if err != nil {
		return TableDetail{}, err
	}
	return markSensitiveColumns(detail), nil
}

func (s *Service) Rows(ctx context.Context, actor identity.Actor, schema string, table string, input RowsInput) (RowsResult, error) {
	if !actor.Can(identity.PermissionDatabaseManage) {
		return RowsResult{}, identity.ErrPermissionDenied
	}

	ref, err := tableRef(schema, table)
	if err != nil {
		return RowsResult{}, err
	}
	detail, err := s.store.TableDetail(ctx, ref)
	if err != nil {
		return RowsResult{}, err
	}
	detail = markSensitiveColumns(detail)

	normalized, err := normalizeRowsInput(input, detail, maxPerPage)
	if err != nil {
		return RowsResult{}, err
	}
	rows, hasNext, err := s.store.TableRows(ctx, ref, detail, normalized)
	if err != nil {
		return RowsResult{}, err
	}

	return RowsResult{
		Columns: detail.Columns,
		Rows:    maskRows(detail, rows),
		Page:    normalized.Page,
		PerPage: normalized.PerPage,
		HasNext: hasNext,
	}, nil
}

func (s *Service) Reveal(ctx context.Context, actor identity.Actor, schema string, table string, input RevealInput) (RevealResult, error) {
	if !actor.Can(identity.PermissionDatabaseManage) {
		return RevealResult{}, identity.ErrPermissionDenied
	}

	ref, err := tableRef(schema, table)
	if err != nil {
		return RevealResult{}, err
	}
	detail, err := s.store.TableDetail(ctx, ref)
	if err != nil {
		return RevealResult{}, err
	}
	detail = markSensitiveColumns(detail)
	if len(detail.PrimaryKey) == 0 {
		return RevealResult{}, ErrRevealUnavailable
	}

	column, ok := findColumn(detail, input.Column)
	if !ok {
		return RevealResult{}, ErrInvalidColumn
	}
	if !column.IsSensitive {
		return RevealResult{}, ErrInvalidColumn
	}

	rowKeyValues, err := decodeRowKey(input.RowKey)
	if err != nil {
		return RevealResult{}, ErrRevealUnavailable
	}
	for _, key := range detail.PrimaryKey {
		if _, ok := rowKeyValues[key]; !ok {
			return RevealResult{}, ErrRevealUnavailable
		}
	}
	input.RowKeyValues = rowKeyValues
	value, err := s.store.RevealCell(ctx, ref, detail, input)
	if err != nil {
		return RevealResult{}, err
	}

	return RevealResult{
		Schema: ref.Schema,
		Table:  ref.Name,
		Column: column.Name,
		Value:  normalizeValue(value),
	}, nil
}

func (s *Service) ExportCSV(ctx context.Context, actor identity.Actor, schema string, table string, input RowsInput) ([]byte, error) {
	if !actor.Can(identity.PermissionDatabaseManage) {
		return nil, identity.ErrPermissionDenied
	}

	ref, err := tableRef(schema, table)
	if err != nil {
		return nil, err
	}
	detail, err := s.store.TableDetail(ctx, ref)
	if err != nil {
		return nil, err
	}
	detail = markSensitiveColumns(detail)
	input.Page = 1
	if input.PerPage <= 0 || input.PerPage > maxExportRows {
		input.PerPage = maxExportRows
	}
	normalized, err := normalizeRowsInput(input, detail, maxExportRows)
	if err != nil {
		return nil, err
	}
	rows, _, err := s.store.TableRows(ctx, ref, detail, normalized)
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	header := make([]string, 0, len(detail.Columns))
	for _, column := range detail.Columns {
		header = append(header, column.Name)
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	for _, row := range maskRows(detail, rows) {
		record := make([]string, 0, len(detail.Columns))
		for _, column := range detail.Columns {
			record = append(record, cellString(row.Values[column.Name].Value))
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func tableRef(schema string, table string) (TableRef, error) {
	ref := TableRef{
		Schema: strings.TrimSpace(schema),
		Name:   strings.TrimSpace(table),
	}
	if ref.Schema == "" || ref.Name == "" || isSystemSchema(ref.Schema) {
		return TableRef{}, ErrInvalidTable
	}
	return ref, nil
}

func isSystemSchema(schema string) bool {
	return schema == "pg_catalog" || schema == "information_schema" || strings.HasPrefix(schema, "pg_toast")
}

func markSensitiveColumns(detail TableDetail) TableDetail {
	primary := make(map[string]bool, len(detail.PrimaryKey))
	for _, key := range detail.PrimaryKey {
		primary[key] = true
	}
	for index := range detail.Columns {
		detail.Columns[index].IsPrimaryKey = primary[detail.Columns[index].Name]
		detail.Columns[index].IsSensitive = isSensitiveColumn(detail.Columns[index].Name)
	}
	return detail
}

func isSensitiveColumn(name string) bool {
	normalized := strings.ToLower(name)
	needles := []string{
		"password",
		"secret",
		"token",
		"credential",
		"session",
		"cookie",
		"hash",
		"salt",
		"private_key",
		"access_key",
		"refresh",
	}
	return slices.ContainsFunc(needles, func(needle string) bool {
		return strings.Contains(normalized, needle)
	})
}

// maxRowsPage 限制 DB 浏览器列表的最大页数（M6），避免深 OFFSET 全表扫描 DoS。
const maxRowsPage = 200

func normalizeRowsInput(input RowsInput, detail TableDetail, maxRows int) (RowsInput, error) {
	if input.Page <= 0 {
		input.Page = 1
	}
	// M6：限制最大页数。
	if input.Page > maxRowsPage {
		input.Page = maxRowsPage
	}
	if input.PerPage <= 0 {
		input.PerPage = defaultPerPage
	}
	if input.PerPage > maxRows {
		input.PerPage = maxRows
	}

	input.Sort = strings.TrimSpace(input.Sort)
	input.Direction = strings.ToLower(strings.TrimSpace(input.Direction))
	if input.Direction != "desc" {
		input.Direction = "asc"
	}
	if input.Sort != "" {
		if _, ok := findColumn(detail, input.Sort); !ok {
			return RowsInput{}, ErrInvalidColumn
		}
	}

	input.FilterColumn = strings.TrimSpace(input.FilterColumn)
	input.FilterOperator = strings.ToLower(strings.TrimSpace(input.FilterOperator))
	input.FilterValue = strings.TrimSpace(input.FilterValue)
	if input.FilterColumn == "" {
		input.FilterOperator = ""
		input.FilterValue = ""
		return input, nil
	}
	if _, ok := findColumn(detail, input.FilterColumn); !ok {
		return RowsInput{}, ErrInvalidColumn
	}
	if input.FilterOperator == "" {
		input.FilterOperator = "contains"
	}
	switch input.FilterOperator {
	case "eq", "contains", "is_null", "not_null":
		return input, nil
	default:
		return RowsInput{}, ErrInvalidFilter
	}
}

func findColumn(detail TableDetail, name string) (Column, bool) {
	for _, column := range detail.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return Column{}, false
}

func maskRows(detail TableDetail, rows []map[string]any) []TableRow {
	masked := make([]TableRow, 0, len(rows))
	for _, row := range rows {
		values := make(map[string]CellValue, len(detail.Columns))
		for _, column := range detail.Columns {
			value := normalizeValue(row[column.Name])
			if column.IsSensitive && value != nil {
				values[column.Name] = CellValue{Value: SensitiveMask, Sensitive: true, Masked: true}
				continue
			}
			values[column.Name] = CellValue{Value: value, Sensitive: column.IsSensitive}
		}
		masked = append(masked, TableRow{
			RowKey: rowKey(detail, row),
			Values: values,
		})
	}
	return masked
}

func rowKey(detail TableDetail, row map[string]any) string {
	if len(detail.PrimaryKey) == 0 {
		return ""
	}
	values := make(map[string]string, len(detail.PrimaryKey))
	for _, key := range detail.PrimaryKey {
		value, ok := row[key]
		if !ok || value == nil {
			return ""
		}
		values[key] = cellString(normalizeValue(value))
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeRowKey(value string) (map[string]string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	var values map[string]string
	if err := json.Unmarshal(decoded, &values); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, ErrRevealUnavailable
	}
	return values, nil
}

func normalizeValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano)
	case []byte:
		return string(typed)
	case string, bool:
		return typed
	case int:
		return typed
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return float64(typed)
	case float64:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func cellString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

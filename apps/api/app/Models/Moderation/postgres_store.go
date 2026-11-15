package moderation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool          *pgxpool.Pool
	notifications DecisionNotificationWriter
}

type DecisionNotificationInput struct {
	DecisionID               int64
	TargetType               string
	TargetID, ReviewerUserID int64
	Approved                 bool
	ReviewNote               string
}
type DecisionNotificationWriter interface {
	NotifyModerationTx(context.Context, pgx.Tx, DecisionNotificationInput) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) WithDecisionNotifications(writer DecisionNotificationWriter) *PostgresStore {
	s.notifications = writer
	return s
}

func (s *PostgresStore) GetSettings(ctx context.Context) (Settings, error) {
	return scanSettings(s.pool.QueryRow(ctx, `
		SELECT mode, review_new_users, new_user_max_age_days, review_external_links,
		  updated_by_user_id, updated_at
		FROM moderation_settings
		WHERE singleton = TRUE
	`))
}

func (s *PostgresStore) SaveSettings(ctx context.Context, settings Settings, actorUserID int64) (Settings, error) {
	return s.writeSettings(ctx, settings, actorUserID)
}

func (s *PostgresStore) ResetSettings(ctx context.Context, settings Settings, actorUserID int64) (Settings, error) {
	return s.writeSettings(ctx, settings, actorUserID)
}

func (s *PostgresStore) writeSettings(ctx context.Context, settings Settings, actorUserID int64) (Settings, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO moderation_settings (
		  singleton, mode, review_new_users, new_user_max_age_days,
		  review_external_links, updated_by_user_id, updated_at
		) VALUES (TRUE, $1, $2, $3, $4, $5, now())
		ON CONFLICT (singleton) DO UPDATE SET
		  mode = EXCLUDED.mode,
		  review_new_users = EXCLUDED.review_new_users,
		  new_user_max_age_days = EXCLUDED.new_user_max_age_days,
		  review_external_links = EXCLUDED.review_external_links,
		  updated_by_user_id = EXCLUDED.updated_by_user_id,
		  updated_at = now()
		RETURNING mode, review_new_users, new_user_max_age_days, review_external_links,
		  updated_by_user_id, updated_at
	`, settings.Mode, settings.ReviewNewUsers, settings.NewUserMaxAgeDays,
		settings.ReviewExternalLinks, nullableReporter(actorUserID))
	return scanSettings(row)
}

func scanSettings(row reportScanner) (Settings, error) {
	var settings Settings
	var updatedBy sql.NullInt64
	if err := row.Scan(
		&settings.Mode,
		&settings.ReviewNewUsers,
		&settings.NewUserMaxAgeDays,
		&settings.ReviewExternalLinks,
		&updatedBy,
		&settings.UpdatedAt,
	); err != nil {
		return Settings{}, fmt.Errorf("scan moderation settings: %w", err)
	}
	if updatedBy.Valid {
		value := updatedBy.Int64
		settings.UpdatedByUserID = &value
	}
	return settings, nil
}

func (s *PostgresStore) CreateReport(ctx context.Context, input CreateReportInput) (Report, error) {
	row := s.pool.QueryRow(ctx, `
		WITH created_report AS (
			INSERT INTO moderation_reports (reporter_user_id, target_type, target_id, reason_code, body)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING *
		)
		SELECT `+reportProjection("created_report")+`
		FROM created_report
		LEFT JOIN users reporter ON reporter.id = created_report.reporter_user_id
		LEFT JOIN users reviewer ON reviewer.id = created_report.reviewer_user_id
	`, nullableReporter(input.ReporterUserID), input.TargetType, input.TargetID, input.ReasonCode, input.Body)
	report, err := scanReport(row)
	if err != nil {
		// 唯一索引冲突：同一举报者对同一目标已有 open 举报。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Report{}, ErrReportDuplicate
		}
		return Report{}, fmt.Errorf("create report: %w", err)
	}
	return report, nil
}

func (s *PostgresStore) ListReports(ctx context.Context, input ReportListInput) (ReportList, error) {
	where := "WHERE 1=1"
	args := []any{}
	argIndex := 1
	if input.Status != "" {
		where += fmt.Sprintf(" AND moderation_reports.status = $%d", argIndex)
		args = append(args, input.Status)
		argIndex++
	}
	if input.TargetType != "" {
		where += fmt.Sprintf(" AND moderation_reports.target_type = $%d", argIndex)
		args = append(args, input.TargetType)
		argIndex++
	}
	if input.ReporterID > 0 {
		where += fmt.Sprintf(" AND moderation_reports.reporter_user_id = $%d", argIndex)
		args = append(args, input.ReporterID)
		argIndex++
	}

	var total int64
	countArgs := append([]any{}, args...)
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM moderation_reports
	`+where, countArgs...).Scan(&total); err != nil {
		return ReportList{}, fmt.Errorf("count reports: %w", err)
	}

	args = append(args, input.PerPage, (input.Page-1)*input.PerPage)
	rows, err := s.pool.Query(ctx, reportSelectSQL()+where+`
		ORDER BY moderation_reports.created_at DESC, moderation_reports.id DESC
		LIMIT $`+itoa(argIndex)+` OFFSET $`+itoa(argIndex+1)+`
	`, args...)
	if err != nil {
		return ReportList{}, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()

	items := []Report{}
	for rows.Next() {
		report, err := scanReport(rows)
		if err != nil {
			return ReportList{}, err
		}
		items = append(items, report)
	}
	if err := rows.Err(); err != nil {
		return ReportList{}, fmt.Errorf("iterate reports: %w", err)
	}
	return ReportList{Items: items, Total: total, Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *PostgresStore) GetReport(ctx context.Context, reportID int64) (Report, error) {
	row := s.pool.QueryRow(ctx, reportSelectSQL()+`
		WHERE moderation_reports.id = $1
	`, reportID)
	report, err := scanReport(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, ErrReportNotFound
	}
	if err != nil {
		return Report{}, fmt.Errorf("get report: %w", err)
	}
	return report, nil
}

func (s *PostgresStore) UpdateReport(ctx context.Context, input UpdateReportInput) (Report, error) {
	// 四条不变式全部压进同一条 UPDATE 的谓词与表达式里，而不是「先读状态再写」：
	// 单语句在行锁内完成判定与写入，检查与使用之间没有窗口，并发两次写只有一次命中。
	//
	//   1. 终态守卫：status NOT IN (resolved, rejected) —— 结案结论不可回退、不可改写。
	//   2. 版本 CAS：($6 = 0 OR revision = $6) —— 0 表示旧客户端未声明期望版本。
	//   3. resolved_at 只写一次：COALESCE(现有值, now())，首次进入终态才落时间。
	//   4. reviewer 首占不被覆盖：非 moderation.manage 只能填充空缺，不能改派。
	row := s.pool.QueryRow(ctx, `
		WITH updated_report AS (
			UPDATE moderation_reports
			SET status = $2,
			    reviewer_user_id = CASE
			      WHEN $4::boolean THEN $3::bigint
			      ELSE COALESCE(moderation_reports.reviewer_user_id, $3::bigint)
			    END,
			    review_note = $5,
			    resolved_at = CASE
			      WHEN $2 IN ('resolved', 'rejected')
			        THEN COALESCE(moderation_reports.resolved_at, now())
			      ELSE moderation_reports.resolved_at
			    END,
			    revision = moderation_reports.revision + 1,
			    updated_at = now()
			WHERE id = $1
			  AND status NOT IN ('resolved', 'rejected')
			  AND ($6::bigint = 0 OR revision = $6::bigint)
			RETURNING *
		)
		SELECT `+reportProjection("updated_report")+`
		FROM updated_report
		LEFT JOIN users reporter ON reporter.id = updated_report.reporter_user_id
		LEFT JOIN users reviewer ON reviewer.id = updated_report.reviewer_user_id
	`, input.ReportID, input.Status, nullableReporter(input.ReviewerUserID),
		input.AllowReviewerOverride, input.ReviewNote, input.ExpectedRevision)
	report, err := scanReport(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Report{}, classifyReportUpdateMiss(ctx, s.pool, input)
	}
	if err != nil {
		return Report{}, fmt.Errorf("update report: %w", err)
	}
	return report, nil
}

// classifyReportUpdateMiss 把「UPDATE 命中 0 行」翻译成领域错误。
//
// 三类原因对应三种修复动作：举报不存在（换 id）、已结案（不能再改）、版本过期
// （重新读取后再写）。合并成一个错误会让调用方无法自愈，也会让 404 与 409 混淆。
//
// 这里是尽力而为的分类，不是强一致快照：UPDATE 已经未命中，之后这次独立查询读到
// 什么就报什么 —— 并发结案会让它报 ErrReportTerminal，并发硬删会报
// ErrReportNotFound，只有读到活跃态才报 ErrReportConflict。它描述的是「读那一刻的
// 行状态」，用途是给调用方一个可行动的修复方向，不要当成事务内的判定依据。
func classifyReportUpdateMiss(ctx context.Context, pool *pgxpool.Pool, input UpdateReportInput) error {
	var status string
	var revision int64
	err := pool.QueryRow(ctx, `
		SELECT status, revision FROM moderation_reports WHERE id = $1
	`, input.ReportID).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReportNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect report after rejected update: %w", err)
	}
	if isTerminalStatus(status) {
		return ErrReportTerminal
	}
	return ErrReportConflict
}

func reportSelectSQL() string {
	return `
			SELECT ` + reportProjection("moderation_reports") + `
			FROM moderation_reports
			LEFT JOIN users reporter ON reporter.id = moderation_reports.reporter_user_id
			LEFT JOIN users reviewer ON reviewer.id = moderation_reports.reviewer_user_id
		`
}

func reportProjection(source string) string {
	return fmt.Sprintf(`%[1]s.id, %[1]s.reporter_user_id,
		reporter.username, reporter.display_name,
		%[1]s.target_type, %[1]s.target_id,
		%[1]s.reason_code, %[1]s.body, %[1]s.status, %[1]s.revision,
		%[1]s.reviewer_user_id,
		reviewer.username, reviewer.display_name,
		%[1]s.review_note,
		%[1]s.created_at, %[1]s.updated_at, %[1]s.resolved_at`, source)
}

type reportScanner interface {
	Scan(dest ...any) error
}

func scanReport(row reportScanner) (Report, error) {
	var report Report
	var reporterID sql.NullInt64
	var reporterUsername sql.NullString
	var reporterDisplay sql.NullString
	var reviewerID sql.NullInt64
	var reviewerUsername sql.NullString
	var reviewerDisplay sql.NullString
	var resolvedAt sql.NullTime
	if err := row.Scan(
		&report.ID,
		&reporterID,
		&reporterUsername,
		&reporterDisplay,
		&report.TargetType,
		&report.TargetID,
		&report.ReasonCode,
		&report.Body,
		&report.Status,
		&report.Revision,
		&reviewerID,
		&reviewerUsername,
		&reviewerDisplay,
		&report.ReviewNote,
		&report.CreatedAt,
		&report.UpdatedAt,
		&resolvedAt,
	); err != nil {
		return Report{}, err
	}
	if reporterID.Valid {
		report.ReporterUserID = reporterID.Int64
		report.ReporterName = displayName(reporterUsername, reporterDisplay)
	}
	if reviewerID.Valid {
		id := reviewerID.Int64
		report.ReviewerUserID = &id
		report.ReviewerName = displayName(reviewerUsername, reviewerDisplay)
	}
	if resolvedAt.Valid {
		t := resolvedAt.Time
		report.ResolvedAt = &t
	}
	return report, nil
}

func displayName(username, display sql.NullString) string {
	if display.Valid && display.String != "" {
		return display.String
	}
	if username.Valid && username.String != "" {
		return username.String
	}
	return ""
}

func nullableReporter(userID int64) any {
	if userID <= 0 {
		return nil
	}
	return userID
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	// 简单整数转字符串，避免引入 strconv 仅为单处使用。
	result := ""
	n := value
	for n > 0 {
		result = string(digits[n%10]) + result
		n /= 10
	}
	return result
}

package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

var (
	ErrNotFound         = errors.New("jobs: not found")
	ErrScheduleNotFound = errors.New("jobs: schedule not found")
	ErrScheduleDisabled = errors.New("jobs: schedule is disabled")
)

// OptionStore 读写 schedule 启用状态（通常是 web_options）。
type OptionStore interface {
	// Get 返回 name 对应 value；ok=false 表示缺失。
	Get(ctx context.Context, name string) (value string, ok bool, err error)
	// Set 写入 name=value。
	Set(ctx context.Context, name, value string) error
}

// ScheduleConstructor 构造某条 schedule 对应的 River job args（与 worker periodics 同源）。
type ScheduleConstructor func() (river.JobArgs, *river.InsertOpts)

type Service struct {
	pool         *pgxpool.Pool
	client       *supportjobs.Client
	options      OptionStore
	constructors map[string]ScheduleConstructor
	// nowFn 便于单测注入时钟；生产为 time.Now。
	nowFn func() time.Time
}

func NewService(pool *pgxpool.Pool, client *supportjobs.Client) *Service {
	return &Service{
		pool:         pool,
		client:       client,
		constructors: map[string]ScheduleConstructor{},
		nowFn:        time.Now,
	}
}

// WithScheduleOptions 注入 option 存储与手动触发用的 constructors。
func (s *Service) WithScheduleOptions(options OptionStore, constructors map[string]ScheduleConstructor) *Service {
	s.options = options
	if constructors != nil {
		s.constructors = constructors
	}
	return s
}

func (s *Service) Overview(ctx context.Context, actor identity.Actor) (Overview, error) {
	if !actor.Can(identity.PermissionJobsView) {
		return Overview{}, identity.ErrPermissionDenied
	}
	rows, err := s.pool.Query(ctx, `SELECT state::text, count(*) FROM river_job GROUP BY state`)
	if err != nil {
		return Overview{}, err
	}
	defer rows.Close()
	result := Overview{Counts: map[string]int64{}}
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return Overview{}, err
		}
		result.Counts[state] = count
	}
	queues, err := s.queues(ctx)
	if err != nil {
		return Overview{}, err
	}
	result.Queues = queues
	return result, rows.Err()
}

func (s *Service) List(ctx context.Context, actor identity.Actor, input ListInput) ([]Job, error) {
	if !actor.Can(identity.PermissionJobsView) {
		return nil, identity.ErrPermissionDenied
	}
	limit := input.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	params := river.NewJobListParams().First(limit).OrderBy(river.JobListOrderByID, river.SortOrderDesc)
	if input.Queue != "" {
		params = params.Queues(input.Queue)
	}
	if input.Kind != "" {
		params = params.Kinds(input.Kind)
	}
	if input.State != "" {
		params = params.States(rivertype.JobState(input.State))
	}
	result, err := s.client.JobList(ctx, params)
	if err != nil {
		return nil, err
	}
	items := make([]Job, len(result.Jobs))
	for i, row := range result.Jobs {
		items[i] = mapJob(row)
	}
	return items, nil
}

func (s *Service) Detail(ctx context.Context, actor identity.Actor, id int64) (Job, error) {
	if !actor.Can(identity.PermissionJobsView) {
		return Job{}, identity.ErrPermissionDenied
	}
	row, err := s.client.JobGet(ctx, id)
	if errors.Is(err, rivertype.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return mapJob(row), nil
}

func (s *Service) Retry(ctx context.Context, actor identity.Actor, id int64) (Job, error) {
	if !actor.Can(identity.PermissionJobsManage) {
		return Job{}, identity.ErrPermissionDenied
	}
	row, err := s.client.JobRetry(ctx, id)
	if errors.Is(err, rivertype.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return mapJob(row), nil
}

func (s *Service) Cancel(ctx context.Context, actor identity.Actor, id int64) (Job, error) {
	if !actor.Can(identity.PermissionJobsManage) {
		return Job{}, identity.ErrPermissionDenied
	}
	row, err := s.client.JobCancel(ctx, id)
	if errors.Is(err, rivertype.ErrNotFound) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return mapJob(row), nil
}

func (s *Service) SetQueuePaused(ctx context.Context, actor identity.Actor, name string, paused bool) error {
	if !actor.Can(identity.PermissionJobsManage) {
		return identity.ErrPermissionDenied
	}
	if name == "" {
		return fmt.Errorf("queue name is required")
	}
	if paused {
		return s.client.QueuePause(ctx, name, nil)
	}
	return s.client.QueueResume(ctx, name, nil)
}

// Schedules 返回宿主 schedule catalog，并附带运行时启用状态与 last/next run。
func (s *Service) Schedules(ctx context.Context, actor identity.Actor) ([]Schedule, error) {
	if !actor.Can(identity.PermissionJobsView) {
		return nil, identity.ErrPermissionDenied
	}
	defs := supportjobs.CoreScheduleDefinitions()
	lastRuns, err := s.lastRunsByKind(ctx, defs)
	if err != nil {
		// last run 失败不拖垮整个列表；运维仍能看目录与启用状态。
		lastRuns = map[string]time.Time{}
	}
	now := s.nowFn().UTC()
	out := make([]Schedule, 0, len(defs))
	for _, def := range defs {
		item := scheduleFromDef(def)
		item.Enabled = s.scheduleEnabled(ctx, def.ID, def.Enabled)
		if last, ok := lastRuns[def.JobKind]; ok {
			t := last.UTC()
			item.LastRunAt = &t
		}
		if next := estimateNextRun(item.Enabled, def.Interval, item.LastRunAt, now); next != nil {
			item.NextRunAt = next
		}
		out = append(out, item)
	}
	return out, nil
}

// SetScheduleEnabled 持久化启用状态；worker constructor 下次周期会读取。
func (s *Service) SetScheduleEnabled(ctx context.Context, actor identity.Actor, scheduleID string, enabled bool) (Schedule, error) {
	if !actor.Can(identity.PermissionJobsManage) {
		return Schedule{}, identity.ErrPermissionDenied
	}
	if err := supportjobs.ValidateScheduleID(scheduleID); err != nil {
		return Schedule{}, ErrScheduleNotFound
	}
	def, ok := findCoreSchedule(scheduleID)
	if !ok {
		return Schedule{}, ErrScheduleNotFound
	}
	if s.options == nil {
		return Schedule{}, fmt.Errorf("schedule option store is not configured")
	}
	name := supportjobs.ScheduleEnabledOptionName(scheduleID)
	if err := s.options.Set(ctx, name, supportjobs.FormatScheduleEnabled(enabled)); err != nil {
		return Schedule{}, err
	}
	item := scheduleFromDef(def)
	item.Enabled = enabled
	lastRuns, _ := s.lastRunsByKind(ctx, []supportjobs.ScheduleDefinition{def})
	now := s.nowFn().UTC()
	if last, ok := lastRuns[def.JobKind]; ok {
		t := last.UTC()
		item.LastRunAt = &t
	}
	if next := estimateNextRun(item.Enabled, def.Interval, item.LastRunAt, now); next != nil {
		item.NextRunAt = next
	}
	return item, nil
}

// TriggerSchedule 立即入队一次对应 job（不改变周期时钟；next run 仍按 last+interval 估算）。
func (s *Service) TriggerSchedule(ctx context.Context, actor identity.Actor, scheduleID string) (TriggerResult, error) {
	if !actor.Can(identity.PermissionJobsManage) {
		return TriggerResult{}, identity.ErrPermissionDenied
	}
	if err := supportjobs.ValidateScheduleID(scheduleID); err != nil {
		return TriggerResult{}, ErrScheduleNotFound
	}
	def, ok := findCoreSchedule(scheduleID)
	if !ok {
		return TriggerResult{}, ErrScheduleNotFound
	}
	if !s.scheduleEnabled(ctx, def.ID, def.Enabled) {
		return TriggerResult{}, ErrScheduleDisabled
	}
	ctor, ok := s.constructors[scheduleID]
	if !ok || ctor == nil {
		return TriggerResult{}, fmt.Errorf("schedule %q has no constructor", scheduleID)
	}
	args, opts := ctor()
	if args == nil {
		return TriggerResult{}, fmt.Errorf("schedule %q constructor returned nil args", scheduleID)
	}
	if opts == nil {
		opts = &river.InsertOpts{}
	}
	if opts.Queue == "" && def.Queue != "" {
		copied := *opts
		copied.Queue = def.Queue
		opts = &copied
	}
	// 手动触发跳过 Unique 去重：运维期望“立刻再跑一次”，而不是拿到已有排队 job。
	// 周期任务自身仍可带 Unique；手动路径显式清空。
	opts.UniqueOpts = river.UniqueOpts{}

	result, err := s.client.Insert(ctx, args, opts)
	if err != nil {
		return TriggerResult{}, err
	}
	queue := def.Queue
	if result.Job != nil && result.Job.Queue != "" {
		queue = result.Job.Queue
	}
	jobID := int64(0)
	if result.Job != nil {
		jobID = result.Job.ID
	}
	return TriggerResult{
		ScheduleID:    scheduleID,
		JobID:         jobID,
		Kind:          args.Kind(),
		Queue:         queue,
		UniqueSkipped: result.UniqueSkippedAsDuplicate,
	}, nil
}

func (s *Service) scheduleEnabled(ctx context.Context, scheduleID string, catalogDefault bool) bool {
	if s.options == nil {
		return catalogDefault
	}
	value, ok, err := s.options.Get(ctx, supportjobs.ScheduleEnabledOptionName(scheduleID))
	if err != nil || !ok {
		return catalogDefault
	}
	return supportjobs.ParseScheduleEnabled(value, true)
}

func (s *Service) lastRunsByKind(ctx context.Context, defs []supportjobs.ScheduleDefinition) (map[string]time.Time, error) {
	if s.pool == nil || len(defs) == 0 {
		return map[string]time.Time{}, nil
	}
	kinds := make([]string, 0, len(defs))
	for _, def := range defs {
		if def.JobKind != "" {
			kinds = append(kinds, def.JobKind)
		}
	}
	if len(kinds) == 0 {
		return map[string]time.Time{}, nil
	}
	// 取每种 kind 最近一次 created_at 作为“上次触发”近似；
	// River periodics 不单独存 next fire，足够运维监控。
	rows, err := s.pool.Query(ctx, `
		SELECT kind, max(created_at)
		FROM river_job
		WHERE kind = ANY($1)
		GROUP BY kind
	`, kinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var kind string
		var at time.Time
		if err := rows.Scan(&kind, &at); err != nil {
			return nil, err
		}
		out[kind] = at
	}
	return out, rows.Err()
}

func (s *Service) queues(ctx context.Context) ([]Queue, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT q.name, q.paused_at, q.updated_at,
		 count(j.id) FILTER (WHERE j.state IN ('available','scheduled','retryable')),
		 count(j.id) FILTER (WHERE j.state = 'running'),
		 count(j.id) FILTER (WHERE j.state = 'discarded')
		FROM river_queue q LEFT JOIN river_job j ON j.queue = q.name
		GROUP BY q.name, q.paused_at, q.updated_at ORDER BY q.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Queue{}
	for rows.Next() {
		var item Queue
		if err := rows.Scan(&item.Name, &item.PausedAt, &item.UpdatedAt, &item.Available, &item.Running, &item.Failed); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scheduleFromDef(def supportjobs.ScheduleDefinition) Schedule {
	item := Schedule{
		ID:          def.ID,
		JobKind:     def.JobKind,
		Queue:       def.Queue,
		Cron:        def.Cron,
		Owner:       def.Owner,
		Enabled:     def.Enabled,
		Description: def.Description,
		RunOnStart:  def.RunOnStart,
	}
	if def.Interval > 0 {
		item.IntervalSeconds = int64(def.Interval / time.Second)
	}
	return item
}

func findCoreSchedule(id string) (supportjobs.ScheduleDefinition, bool) {
	for _, def := range supportjobs.CoreScheduleDefinitions() {
		if def.ID == id {
			return def, true
		}
	}
	return supportjobs.ScheduleDefinition{}, false
}

// estimateNextRun：有 last 时 last+interval；无 last 且启用时用 now+interval（粗估，非 River 精确时钟）。
func estimateNextRun(enabled bool, interval time.Duration, last *time.Time, now time.Time) *time.Time {
	if !enabled || interval <= 0 {
		return nil
	}
	var next time.Time
	if last != nil && !last.IsZero() {
		next = last.Add(interval)
		// 若已过期（任务积压/刚恢复），显示“尽快”，用 now 作为近似。
		if next.Before(now) {
			next = now
		}
	} else {
		next = now.Add(interval)
	}
	next = next.UTC()
	return &next
}

func mapJob(row *rivertype.JobRow) Job {
	return Job{ID: row.ID, Kind: row.Kind, Queue: row.Queue, State: string(row.State), Attempt: row.Attempt, MaxAttempts: row.MaxAttempts, Priority: row.Priority, Args: append([]byte(nil), row.EncodedArgs...), Metadata: append([]byte(nil), row.Metadata...), Tags: row.Tags, Errors: row.Errors, AttemptedBy: row.AttemptedBy, CreatedAt: row.CreatedAt, ScheduledAt: row.ScheduledAt, AttemptedAt: row.AttemptedAt, FinalizedAt: row.FinalizedAt}
}

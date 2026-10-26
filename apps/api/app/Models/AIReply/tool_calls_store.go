package aireply

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// ErrToolCallsUnavailable 表示工具调用记录能力没有装配（pool 缺失）。
var ErrToolCallsUnavailable = errors.New("aireply: tool call store is unavailable")

// ToolCall 是一次 AI 工具调用的持久形态。
//
// 它与 supportai.ToolTraceEntry 一一对应，只多一个 Seq：trace 里的 Step 是
// 「第几次模型调用」，同一条 assistant 消息里的并行调用共享它，无法充当幂等键。
// Seq 是 trace 切片下标，在一次 run 内唯一。
//
// 字段名刻意与 trace 保持不同（Seq/Step 而非 Step/ModelCalls），因为它描述的是
// 「落库的一行」，不是「运行期的一条轨迹」——两者混淆会让后来者以为可以直接把
// 结构体塞进 INSERT。
type ToolCall struct {
	Seq            int
	Step           int
	Tool           string
	Arguments      string
	ArgumentsBytes int
	ResultBytes    int
	ResultDigest   string
	IsError        bool
	Failure        string
	LatencyMS      int
}

// ToolCallRecorder 把一次 run 的工具轨迹落到产出评论名下。
//
// topicID 冗余存在表里，是因为公开读的可见性判定需要先按评论找到主题；把它一起
// 写下来让审计查询不必再 join 一次 comments。
type ToolCallRecorder interface {
	Record(ctx context.Context, sourceCommentID, topicID int64, runID string, calls []ToolCall) error
}

// ToolCallReader 按 AI 产出评论读取工具调用，保持写入顺序。
//
// 读取时不重新校验「这条调用是否仍然成立」：调用发生过就是历史事实，与依据
// （ai_citations）不同，它没有「被引用对象失效」这种二次失效语义。可见性由
// 调用方（Forum 的公开读服务）在读取之前用主题级谓词判定。
type ToolCallReader interface {
	ListBySourceComment(ctx context.Context, sourceCommentID int64) ([]ToolCall, error)
	// ListByRun 供运营者回放：返回该 run 的全部调用（含被拒绝的结局）。
	ListByRun(ctx context.Context, runID string) ([]ToolCall, error)
}

// PostgresToolCallStore 是工具轨迹的唯一持久化实现。
type PostgresToolCallStore struct {
	pool *pgxpool.Pool
}

func NewPostgresToolCallStore(pool *pgxpool.Pool) *PostgresToolCallStore {
	return &PostgresToolCallStore{pool: pool}
}

var (
	_ ToolCallRecorder = (*PostgresToolCallStore)(nil)
	_ ToolCallReader   = (*PostgresToolCallStore)(nil)
)

// Record 写入一次 run 的工具轨迹。
//
// 幂等：ON CONFLICT (comment_id, seq) DO NOTHING。River 的重试是整任务重跑，
// 而重跑只有在「发布失败」时才会发生；即便如此，重复写入也不该产生重复行——
// 一次发布对应一份轨迹，这是幂等键要表达的语义。
//
// 条数上限取 MaxToolCallsPerReply：它不是渲染上限（渲染上限是前端的折叠块），
// 而是「一次 run 最多能有多少条调用」的宿主硬顶，超出的部分本来也不会被执行。
func (s *PostgresToolCallStore) Record(ctx context.Context, sourceCommentID, topicID int64, runID string, calls []ToolCall) error {
	if s == nil || s.pool == nil {
		return ErrToolCallsUnavailable
	}
	if sourceCommentID <= 0 {
		return errors.New("aireply: tool call record requires the produced comment id")
	}
	if len(calls) == 0 {
		return nil
	}
	if len(calls) > supportai.MaxToolCallsPerReply {
		calls = calls[:supportai.MaxToolCallsPerReply]
	}
	batch := &pgx.Batch{}
	queued := 0
	for _, call := range calls {
		tool := strings.TrimSpace(call.Tool)
		if tool == "" {
			// 没有工具名的行无法解释任何事：模型没给出名字时轨迹本身就没有意义。
			continue
		}
		batch.Queue(`
			INSERT INTO ai_tool_calls (
				run_id, topic_id, comment_id, seq, step, tool, arguments,
				arguments_bytes, result_bytes, result_digest, is_error, failure, latency_ms
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (comment_id, seq) DO NOTHING
		`, strings.TrimSpace(runID), topicID, sourceCommentID, call.Seq, call.Step, tool,
			call.Arguments, call.ArgumentsBytes, call.ResultBytes, call.ResultDigest,
			call.IsError, call.Failure, call.LatencyMS)
		queued++
	}
	if queued == 0 {
		return nil
	}
	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for index := 0; index < queued; index++ {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("record tool call %d: %w", index, err)
		}
	}
	return nil
}

const toolCallColumns = `seq, step, tool, arguments, arguments_bytes, result_bytes,
	result_digest, is_error, failure, latency_ms`

// ListBySourceComment 按产出评论读取工具调用，顺序即写入顺序。
func (s *PostgresToolCallStore) ListBySourceComment(ctx context.Context, sourceCommentID int64) ([]ToolCall, error) {
	if s == nil || s.pool == nil {
		return nil, ErrToolCallsUnavailable
	}
	if sourceCommentID <= 0 {
		return nil, nil
	}
	return s.query(ctx, `
		SELECT `+toolCallColumns+`
		FROM ai_tool_calls
		WHERE comment_id = $1
		ORDER BY seq ASC
	`, sourceCommentID)
}

// ListByRun 按 run 读取工具调用，供运营者回放。
func (s *PostgresToolCallStore) ListByRun(ctx context.Context, runID string) ([]ToolCall, error) {
	if s == nil || s.pool == nil {
		return nil, ErrToolCallsUnavailable
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	return s.query(ctx, `
		SELECT `+toolCallColumns+`
		FROM ai_tool_calls
		WHERE run_id = $1
		ORDER BY id ASC
	`, runID)
}

func (s *PostgresToolCallStore) query(ctx context.Context, sql string, arg any) ([]ToolCall, error) {
	rows, err := s.pool.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("list tool calls: %w", err)
	}
	defer rows.Close()
	collected := make([]ToolCall, 0, supportai.MaxToolCallsPerReply)
	for rows.Next() {
		var call ToolCall
		if err := rows.Scan(&call.Seq, &call.Step, &call.Tool, &call.Arguments,
			&call.ArgumentsBytes, &call.ResultBytes, &call.ResultDigest,
			&call.IsError, &call.Failure, &call.LatencyMS); err != nil {
			return nil, err
		}
		collected = append(collected, call)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return collected, nil
}

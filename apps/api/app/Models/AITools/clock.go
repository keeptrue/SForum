package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// TimeTool 返回服务器当前时间。
//
// 它是唯一零依赖的工具，也是最容易被忽略的一个：模型没有时钟，「今天几号」
// 「三天前发生了什么」这类问题没有它就只能猜。时区刻意标明是服务器本地时区，
// 而不是假装知道提问者所在时区。
type TimeTool struct {
	clock func() time.Time
}

func NewTimeTool(clock func() time.Time) *TimeTool {
	if clock == nil {
		clock = time.Now
	}
	return &TimeTool{clock: clock}
}

func (t *TimeTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name:        "time-now",
		Description: "返回服务器当前时间（含 UTC）。涉及「今天」「最近」「多久以前」这类相对时间的问题时使用。",
		Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
	}
}

func (t *TimeTool) Invoke(_ context.Context, _ supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	clock := time.Now
	if t != nil && t.clock != nil {
		clock = t.clock
	}
	now := clock()
	content := fmt.Sprintf("服务器本地时间：%s（%s）\nUTC：%s\nUnix 时间戳：%d",
		now.Format(time.RFC3339), weekdayLabel(now), now.UTC().Format(time.RFC3339), now.Unix())
	return supportai.ToolResult{Content: content}, nil
}

func weekdayLabel(value time.Time) string {
	switch value.Weekday() {
	case time.Monday:
		return "星期一"
	case time.Tuesday:
		return "星期二"
	case time.Wednesday:
		return "星期三"
	case time.Thursday:
		return "星期四"
	case time.Friday:
		return "星期五"
	case time.Saturday:
		return "星期六"
	default:
		return "星期日"
	}
}

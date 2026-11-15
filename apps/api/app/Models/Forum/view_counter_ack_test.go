package forum

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Ack 必须是一次原子脚本（RV-52 复审项）。
//
// 旧实现是 DECRBY 再 DEL 两步：若并发 INCR 恰好落在两步之间，DEL 会把它一起删掉，
// 这段浏览就永久丢了。这条用例盯住「宿主只发一条 EVAL」，以及脚本同时承担扣减与清理。
func TestAckViewDeltasIsOneAtomicScript(t *testing.T) {
	client := &recordingViewClient{left: 0}
	counter := &RedisTopicViewCounter{client: client}

	if err := counter.AckViewDeltas(context.Background(), map[int64]int64{7: 5}); err != nil {
		t.Fatalf("ack view deltas: %v", err)
	}
	if len(client.commands) != 1 || client.commands[0] != "eval" {
		t.Fatalf("ack issued %v; the decrement and its cleanup must be one atomic script", client.commands)
	}
	for _, want := range []string{"DECRBY", "DEL", "SREM"} {
		if !strings.Contains(client.script, want) {
			t.Fatalf("ack script does not contain %s: %q", want, client.script)
		}
	}
	if len(client.keys) != 2 || client.keys[0] != "forum:topic:views:delta:7" || client.keys[1] != "forum:topic:views:dirty" {
		t.Fatalf("ack keys = %v", client.keys)
	}
	if len(client.args) != 2 || client.args[0] != int64(5) || client.args[1] != "7" {
		t.Fatalf("ack args = %v, want [5 7]", client.args)
	}
}

// 记账语义：没有增量时不应发出任何命令。
func TestAckViewDeltasSkipsEmptyInput(t *testing.T) {
	client := &recordingViewClient{}
	counter := &RedisTopicViewCounter{client: client}
	if err := counter.AckViewDeltas(context.Background(), map[int64]int64{}); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 0 {
		t.Fatalf("empty ack issued %v", client.commands)
	}
}

// recordingViewClient 记录命令序列，用来区分「一次 EVAL」与「DECRBY + DEL」。
type recordingViewClient struct {
	redis.Scripter
	commands []string
	script   string
	keys     []string
	args     []any
	left     int64
}

func (c *recordingViewClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	c.commands = append(c.commands, "eval")
	c.script = script
	c.keys = keys
	c.args = args
	cmd := redis.NewCmd(ctx)
	cmd.SetVal(c.left)
	return cmd
}

// EvalSha 返回 NOSCRIPT，让 redis.Script.Run 回落到 EVAL。
func (c *recordingViewClient) EvalSha(ctx context.Context, _ string, _ []string, _ ...interface{}) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	cmd.SetErr(redis.ErrNoScript)
	return cmd
}

func (c *recordingViewClient) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd {
	c.commands = append(c.commands, "setnx")
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(true)
	return cmd
}

func (c *recordingViewClient) Incr(ctx context.Context, key string) *redis.IntCmd {
	c.commands = append(c.commands, "incr")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(1)
	return cmd
}

func (c *recordingViewClient) SAdd(ctx context.Context, key string, members ...interface{}) *redis.IntCmd {
	c.commands = append(c.commands, "sadd")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(1)
	return cmd
}

func (c *recordingViewClient) SMembers(ctx context.Context, key string) *redis.StringSliceCmd {
	c.commands = append(c.commands, "smembers")
	cmd := redis.NewStringSliceCmd(ctx)
	cmd.SetVal(nil)
	return cmd
}

func (c *recordingViewClient) Get(ctx context.Context, key string) *redis.StringCmd {
	c.commands = append(c.commands, "get")
	cmd := redis.NewStringCmd(ctx)
	cmd.SetVal("0")
	return cmd
}

func (c *recordingViewClient) DecrBy(ctx context.Context, key string, decrement int64) *redis.IntCmd {
	c.commands = append(c.commands, "decrby")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(c.left)
	return cmd
}

func (c *recordingViewClient) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	c.commands = append(c.commands, "del")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(1)
	return cmd
}

func (c *recordingViewClient) SRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd {
	c.commands = append(c.commands, "srem")
	cmd := redis.NewIntCmd(ctx)
	cmd.SetVal(1)
	return cmd
}

// devguard 在开发库里幂等写入哨兵表 public.sforum_dev_guard。
//
// scripts/api-dev.sh 启动开发 API 前调用它，使 internal/testdb 能把"这是开发库"
// 与"这是一次性测试集群"区分开：只要哨兵在，数据库测试就拒绝连接。
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/internal/testdb/guard"
)

func main() {
	// 哨兵表只属于开发库。生产/预发误跑一次就会往那个库写一张表，
	// 不能只靠"没人会跑"——与 config.Load 的默认值保持一致：空值即 development。
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	if appEnv == "" {
		appEnv = "development"
	}
	if appEnv != "development" {
		fmt.Fprintf(os.Stderr, "devguard: refusing to run outside development (APP_ENV=%q)\n", appEnv)
		os.Exit(1)
	}
	url := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if url == "" {
		fmt.Fprintln(os.Stderr, "devguard: DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := guard.Open(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devguard: connect %s failed: %v\n", guard.Redact(url), err)
		os.Exit(1)
	}
	defer db.Close()
	state, err := guard.Inspect(ctx, db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "devguard: inspect %s failed: %v\n", guard.Redact(url), err)
		os.Exit(1)
	}
	if state.Disposable {
		// 一次性测试集群不需要（也不该有）开发库哨兵。
		fmt.Printf("devguard: %s is a disposable test cluster, no guard written\n", guard.Redact(url))
		return
	}
	if err := guard.EnsureSentinel(ctx, db); err != nil {
		fmt.Fprintf(os.Stderr, "devguard: %s failed: %v\n", guard.SentinelTable, err)
		os.Exit(1)
	}
	fmt.Printf("devguard: %s marked on %s\n", guard.SentinelTable, guard.Redact(url))
}

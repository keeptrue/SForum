package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestBindPostgresProtocolV2CommandRuntimePublishesDelegatedCatalog(t *testing.T) {
	pool := new(pgxpool.Pool)
	gateway := hostapi.NewGateway(nil)
	deferred, err := bindPostgresProtocolV2CommandRuntime(
		gateway,
		pool,
		new(supportjobs.Dispatcher),
		moderation.NewPostgresStore(pool),
		attachments.NewPostgresStore(pool),
		attachments.NewService(attachments.NewPostgresStore(pool), nil),
		extensionreference.NewStore(pool),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gateway.ProtocolV2ActorDelegationIssuer() == nil {
		t.Fatal("expected the production gateway to expose its boot-scoped actor delegation issuer")
	}
	// 目录在事件总线与 AI 网关就绪前就已冻结：两个目标在此刻必须存在且显式报告
	// 不可用，而不是让命令以为自己接线成功。
	if deferred.Events == nil || deferred.AI == nil {
		t.Fatal("expected both deferred Host Command targets")
	}
	if result := deferred.Events.EmitFromExtension(context.Background(), "demo.plugin", appevents.Envelope{Name: "demo.plugin.ping", Kind: "observe"}); result.OK ||
		result.Reason != "events.publisher_unavailable" {
		t.Fatalf("unwired event publisher result = %#v", result)
	}
	if _, err := deferred.AI.Execute(context.Background(), supportai.CompletionRequest{}); !errors.Is(err, supportai.ErrGatewayUnavailable) {
		t.Fatalf("unwired AI gateway must report unavailable, got %v", err)
	}
}

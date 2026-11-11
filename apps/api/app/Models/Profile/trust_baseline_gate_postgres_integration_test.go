package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/internal/trustgate"
)

// N-06：TL0 会员**不能上传头像**——这是 attachment.upload 基线门的**已知且有意**
// 的产品副作用（头像上传与通用附件共用同一个权限键），不是回归。
//
// 有名字地红/绿，是为了让下一个人看到「新人 7 天灰头像」时知道这是设计，
// 而不是把门控当 bug 修掉。

type gateZeroActivity struct{}

func (gateZeroActivity) ActivityFor(context.Context, int64) (trustlevels.Activity, error) {
	return trustlevels.Activity{}, nil
}

type gateDefaultPolicy struct{}

func (gateDefaultPolicy) Policy(context.Context) (trustlevels.Policy, error) {
	return trustlevels.DefaultPolicy(), nil
}

// gateAvatarOptions 让 TL1 的对照调用停在「头像上传被站点策略关闭」而不是
// 权限拒绝上：它证明的是权限门已打开，而不是整条上传链路可用。
type gateAvatarOptions struct{}

func (gateAvatarOptions) AvatarOptions(context.Context) (options.AvatarOptions, error) {
	return options.AvatarOptions{AllowUpload: false}, nil
}

func gateActor(t *testing.T, fixture *trustgate.Fixture, userID int64) identity.Actor {
	t.Helper()
	actor, err := identity.LoadEffectiveActorTx(context.Background(), fixture.BeginTx(t), userID,
		trustlevels.NewResolver(gateZeroActivity{}, gateDefaultPolicy{}))
	if err != nil {
		t.Fatalf("LoadEffectiveActorTx: %v", err)
	}
	return actor
}

func TestTrustGateN06NewcomerCannotUploadAvatar(t *testing.T) {
	fixture := trustgate.New(t)
	ctx := context.Background()

	fixture.InsertMemberUser(t, 31, "human", time.Now().Add(-1*time.Hour))
	newcomer := gateActor(t, fixture, 31)
	_, err := NewService(nil).UploadAvatar(ctx, newcomer, attachments.UploadInput{})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("N-06 失败：TL0 会员仍能上传头像（err=%v，期望 ErrPermissionDenied）", err)
	}

	// 对照：同一构造在 TL1（账龄 8 天）必须不再被权限拒绝。
	fixture.InsertMemberUser(t, 32, "human", time.Now().Add(-8*24*time.Hour))
	member := gateActor(t, fixture, 32)
	_, controlErr := NewServiceWithAvatar(nil, nil, gateAvatarOptions{}).UploadAvatar(ctx, member, attachments.UploadInput{})
	if errors.Is(controlErr, identity.ErrPermissionDenied) {
		t.Fatalf("N-06 对照失败：TL1 仍被权限拒绝（err=%v），减法门变成了永久剥夺", controlErr)
	}
}

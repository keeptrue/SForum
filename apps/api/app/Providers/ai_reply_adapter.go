package providers

import (
	"context"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// BotAccountAdapter 把身份层的机器人账号查询适配成回复判定需要的形状。
// 两个接口描述的是同一件事，但各自只想要自己需要的字段。
type BotAccountAdapter struct {
	Store *identity.PostgresStore
}

func (a BotAccountAdapter) ReplyBotAccount(ctx context.Context) (aireply.BotAccount, bool, error) {
	if a.Store == nil {
		return aireply.BotAccount{}, false, nil
	}
	userID, username, ok, err := a.Store.ReplyBotAccount(ctx)
	if err != nil || !ok {
		return aireply.BotAccount{}, false, err
	}
	return aireply.BotAccount{UserID: userID, Username: username}, true, nil
}

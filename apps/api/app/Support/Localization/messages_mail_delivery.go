package localization

// 邮件投递状态机的稳定 reason 文案（P2-6）。
//
// 单独成文件而不是继续堆进 messages.go：那份目录已经贴着 1000 行硬上限，再往里
// 加键会直接触发架构门（arch boundary）。这里在包初始化时并入主表，键集合与
// 查询行为完全不变；新增同类 reason 请继续加在这个文件。
var mailDeliveryMessages = map[string]map[string]string{
	"zh-CN": {
		"mail.delivery_not_found":             "找不到这条邮件投递记录。",
		"mail.delivery_not_replayable":        "这条投递当前不可重试：只有失败（failed）的投递可以重放。",
		"mail.delivery_replay_enqueue_failed": "重试入队失败，投递已标记为失败，请稍后再试。",
	},
	"en-US": {
		"mail.delivery_not_found":             "That mail delivery record does not exist.",
		"mail.delivery_not_replayable":        "This delivery cannot be retried right now: only failed deliveries can be replayed.",
		"mail.delivery_replay_enqueue_failed": "The retry could not be queued. The delivery was marked failed; try again later.",
	},
}

func init() {
	for locale, table := range mailDeliveryMessages {
		target, ok := messages[locale]
		if !ok {
			continue
		}
		for key, value := range table {
			target[key] = value
		}
	}
}

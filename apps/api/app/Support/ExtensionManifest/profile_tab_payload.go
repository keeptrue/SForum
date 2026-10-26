package extensionmanifest

// ProfileTabContributionPayload 用于 forum.profile.tabs（profileSection）。
// type=extensionRoute：走扩展路由；type=hostLink：仅允许站内相对路径（非 /api）；
// type=l2Widget：引用本 manifest 内已声明的公开 L2 组件（componentId），
// 由资料页 tab 面板的公开 L2 运行时签发/挂载。
// permission 非空时该 tab 只对拥有该权限键的访客出现：宿主在服务端过滤，
// 该字段永不下发前端。
//
// 单独成文件是刻意的：manifest.go 是已到顶的 legacy 文件（架构门禁 cap），
// 资料页 tab 的载荷契约在这里独立演进，不把它继续堆大。
type ProfileTabContributionPayload struct {
	Type        string `json:"type"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Href        string `json:"href,omitempty"`
	ComponentID string `json:"componentId,omitempty"`
	Permission  string `json:"permission,omitempty"`
}

// PayloadProfileTabL2Widget 是 profileSection 内挂载公开 L2 组件的类型判别值。
const PayloadProfileTabL2Widget = "l2Widget"

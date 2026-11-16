// Package rawcore 持有 raw_core 授权面的显式白名单。
//
// 它是 extensionsruntime 的**策略数据**，不是运行时逻辑：白名单是「哪些 Core 表
// 允许插件直接 DML」的唯一来源，签发租约（grant）与校验有效权限（validate）两侧
// 必须读同一份，否则会出现「授了但校验不认」或反过来的半开状态。
//
// 判断标准只有一条：表里的行是**派生数据**——可以由宿主或插件从其它 Core 数据
// 重新生成（例如重建索引），因此被写坏不会造成不可恢复的身份、凭据、会话、信任
// 或审计后果。一切内容表（topics / posts / comments …）与配置表都不在这里：
// 内容变更走事件与 Host Command，配置走 options API，读取走 sforum_core_v1 视图。
// 新增一张表必须在这里显式加一行，并同时补 allowed / denied 测试与决策记录。
package rawcore

// writableTables 是 raw_core 允许 DML 的 Core 表（public schema）。
var writableTables = []string{
	"search_documents",
	"search_index_state",
	"search_reindex_runs",
}

// writableSequences 单独列：pg_class 里表与序列是不同对象，raw_core 的 INSERT
// 依赖这些序列的 USAGE。
var writableSequences = []string{
	"search_reindex_runs_id_seq",
}

// WritableTables 返回白名单表的副本：策略是只读数据，调用方不该拿到可改写的切片。
func WritableTables() []string {
	return append([]string(nil), writableTables...)
}

// WritableSequences 返回白名单序列的副本。
func WritableSequences() []string {
	return append([]string(nil), writableSequences...)
}

// RelationNames 把两张白名单折成集合，避免调用点各写一遍分支。
func RelationNames() (tables, sequences map[string]struct{}) {
	tables = make(map[string]struct{}, len(writableTables))
	for _, name := range writableTables {
		tables[name] = struct{}{}
	}
	sequences = make(map[string]struct{}, len(writableSequences))
	for _, name := range writableSequences {
		sequences[name] = struct{}{}
	}
	return tables, sequences
}

package cachepolicy

import (
	"encoding/json"
	"fmt"
)

// entrySchema 是缓存条目外层信封的版本。改动信封形状必须同时改这里，
// 让旧条目在解码阶段被判为 miss，而不是被误读成值。
const entrySchema = 1

// cacheEntry 是 Host 写进 Cache backend 的外层信封。
//
// 为什么要信封：tag 失效靠 generation 比对，条目必须自带「写入时各 tag 的
// generation」才能在被读到的那一刻判断自己是否已经过期。裸值无法承载这份元数据。
//
// 信封对调用方不可见：Set 编码、Get 解码，调用方拿到的仍是自己写进去的原始字节。
type cacheEntry struct {
	Schema int               `json:"schema"`
	Value  []byte            `json:"value"`
	Gens   map[string]uint64 `json:"generations,omitempty"`
}

func encodeEntry(value []byte, gens map[string]uint64) ([]byte, error) {
	encoded, err := json.Marshal(cacheEntry{Schema: entrySchema, Value: value, Gens: gens})
	if err != nil {
		return nil, fmt.Errorf("encode cache entry: %w", err)
	}
	return encoded, nil
}

// decodeEntry 解出信封。版本不符或字段缺失都算解码失败：调用方按 miss 处理。
func decodeEntry(raw []byte) (cacheEntry, error) {
	var entry cacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return cacheEntry{}, fmt.Errorf("decode cache entry: %w", err)
	}
	if entry.Schema != entrySchema {
		return cacheEntry{}, fmt.Errorf("decode cache entry: unsupported schema %d", entry.Schema)
	}
	return entry, nil
}

# Decision: CachePolicy 的 tag 失效改 Redis generation（P3-11）

## Status

Accepted（2026-11-17）。本条同时定义了故障姿态与「谁能改」的边界。

## Context

`app/Support/CachePolicy` 是 Host 缓存策略运行时：从 Cache Registry 解析隔离键、
读写后端缓存、记录 inspector 指标、按 tag 失效。它此前**没有接线到任何调用方**，
但 `tagIndex`（`map[tag]map[key]struct{}`）是进程内状态：失效时只能删掉「本进程写过
的 key」。

后果与部署形态直接冲突：

- 多副本 / 蓝绿发布下，失效请求打到的副本未必是写过缓存的那个副本。A 副本收到
  `entity.updated` 后只清了自己的索引，B 副本继续用旧值服务，直到条目 TTL 到期。
- 进程重启即丢索引，等于「重启后没人记得哪些 key 需要失效」。
- 失效的**权威**落在进程内存里，任何「换个副本试试」的运维动作都可能看不到效果，
  也没有任何信号说明失效是否真的生效。

`app/Models/Forum/cached_store.go` 已经用 generation 解决过同一类问题（`forum:gen:*`
+ 原子 `IncrementWithTTL`），本决策沿用同一形状，避免仓库里出现两套失效哲学。

## Decision

### 1. 权威：每个 tag 一个单调递增的 generation，存在 Redis

| 环节 | 行为 |
| --- | --- |
| 写入（`Set`） | 先读该条目所有 tag 的当前 generation，连同值一起写进条目信封 |
| 读取（`Get`） | 解出信封里的 generation，与当前值逐项比对；任何一项不等 → 删除该 key 并返回 miss |
| 失效（`Invalidate`） | 对每个 tag `INCR`（带 TTL）；这是唯一的权威动作 |

- 键名 `sforum:cachepolicy:gen:<tag>`，与 `forum:gen:*` 同族但**不共用键空间**：
  两套缓存的失效节奏互不相干。
- generation 是**单调递增**的：Redis 里的计数器只会变大，重启不回退；重启后读到
  的仍是同一个数，因此不会把旧条目「复活」。
- 条目信封 `{"schema":1,"value":<bytes>,"generations":{"tag":n}}` 对调用方不可见：
  `Set` 编码、`Get` 解码，调用方拿到的仍是自己写进去的原始字节。

### 2. 本地 tagIndex 降级为纯加速索引（非权威）

`tagIndex` 保留，但只用来「失效时顺手删掉本进程写过的 key」，尽快回收空间。
即使它为空、或记录的是别的副本写的 key，**正确性只由 generation 保证**。
这满足「内存 map 要么删、要么降为纯缓存（不能是权威状态）」的要求，同时保住
`InvalidateResult.DeletedKeys` 的既有语义（本进程知道的 key 数）。

### 3. Redis 故障姿态（显式）

| 场景 | 姿态 | 理由 |
| --- | --- | --- |
| 写入时读不到 generation | **不写**，返回错误 | 写进去的条目永远无法被 tag 失效，只会变成读不掉的脏条目 |
| 读取时读不到 generation | **不服务**，返回错误（计 miss 语义的失败） | 无法证明新鲜度就不能把它当命中；此时后端通常也已不可用，调用方本来要回源 |
| 失效时递增失败 | **返回错误**，不声称成功 | 静默 no-op 会让旧数据被当成新鲜数据继续服务，且没有任何信号 |
| 信封解不开 / 旧格式裸值 | 删除该 key，按 miss 处理 | 不可校验的值一律不服务 |

### 4. 谁能改这个方案

- 改 generation 的**键名、TTL、递增方式**，或把权威换回进程内状态：必须改本条决策
  并说明多副本下如何保证失效可见。
- 改条目信封形状：必须同时抬 `entrySchema`，让旧条目在解码阶段被判 miss；不允许
  「新旧格式都能解」的兼容分支，那会让一份无法失效的值继续被服务。
- 增加「按 namespace 批量失效」「按前缀删除」等新能力：属于新决策，不在本条范围内。

## Consequences

- **升级窗口**：本方案之前写入的条目是裸值（无信封），升级后会被判为 miss 并删除。
  本服务此前**没有生产调用方**，实际不存在存量；若将来在有存量时再改信封，必须按
  §4 抬 schema。
- **每次命中多一次读**：`Get` 需要读该条目 tag 的 generation（同一后端、同一连接池）。
  这是「跨进程失效可见」的直接成本；要省掉它只能把 generation 编进 key，而那要求
  `PlanKey` 变成带 `ctx` 的异步接口（公共契约变更），本轮不做。
- **ProviderMemory 仍是单进程语义**：generation 跟随后端，memory backend 下失效只在
  本进程可见。这是 provider 语义的一部分，不是本决策的漏洞；多副本部署必须用
  `ProviderRedis`。
- **失效粒度**：tag 失效让所有带该 tag 的条目逻辑过期（懒删除），不再枚举 key。
  `InvalidateResult.DeletedKeys` 因此只统计「本进程索引里顺手删掉的 key」，不代表
  逻辑失效的范围；逻辑失效范围由 `Tags` 表达。
- **Redis 短暂不可用**：读写都 fail closed 成 miss/错误，不会服务不可校验的条目。
  代价是这段时间缓存命中率归零、回源压力上升；收益是不会出现「失效了但还在服务」。

/**
 * 规则判定层：把「实测模型 + 基线」变成一组带规则码的失败项。
 *
 * 这一层**不做任何输出**，也不读 argv —— 失败项、提示、守卫升级记录都通过 ctx 交出去，
 * 由 CLI 决定怎么打印、怎么退出。这样规则可以单独跑（fixture 测试就是把它指向合成树），
 * 输出格式改动也不会碰到判定逻辑。
 *
 * ctx 的形状：`{ fail(rule, message), note(message), upgrades: [], refresh: boolean }`。
 */

import { GUARD_REGISTRY } from './call-graph.mjs';

// 否则新增一条会同时触发计数与集合两条规则，反例测试就说不清「红的原因是哪一条」。
const RATCHET_COUNTS = new Set(['退化区路由数', '退化区已映射operation数']);
const EXACT_COUNTS = new Set(['契约operation数', '实现侧路由数']);

function checkCounts(observation, baseline, ctx) {
  for (const name of [...RATCHET_COUNTS, ...EXACT_COUNTS]) {
    const value = observation.counts[name];
    const expected = baseline.counts[name];
    const message = `基线计数「${name}」=${expected === undefined ? '(缺失)' : expected}，实测=${value}`;
    if (expected === undefined) {
      ctx.fail('ratchet', `基线棘轮键「${name}」缺失 = 防护被关掉。请手写回 "期望计数"（当前实测 ${value}）`);
      continue;
    }
    if (value === expected) continue;
    if (RATCHET_COUNTS.has(name)) {
      if (value > expected) ctx.fail('ratchet', `${message}；棘轮只许降不许升`);
      else ctx.note(`「${name}」从 ${expected} 收紧到 ${value}，可跑 --refresh 落盘`);
      continue;
    }
    if (ctx.refresh) ctx.note(`待刷新：${message}`);
    else ctx.fail('count-drift', `${message}；确认无回归后跑 --refresh 刷新计数`);
  }
}

function guardCodesOf(id) {
  const entry = GUARD_REGISTRY.find((item) => `${item.dir}:${item.name}` === id);
  return entry ? [...entry.codes].sort((a, b) => a - b) : [];
}

/**
 * 端点 → guard 映射。
 *
 * 判定只看**基线里的「允许升级对」表**，不做任何码集合算术。
 *
 * 为什么废弃算术：`LoadActor[401] → ReadActor[401,503]` 的码集合确实变大，但语义是
 * **降级** —— `ReadActor` 是可选主体（无凭据返回零值 Actor + nil，匿名放行），
 * `LoadActor` 是必登（无凭据 401）。用「码变多就是升级」去判，等于把「必须登录」
 * 悄悄换成「匿名可访问」还记成好事。同理 `LoadActor → TrustLevels:requireAdmin`
 * （跨家族）也会被算术放行。
 *
 * 所以放行只认显式登记的对：`允许升级对` 里每一对都要人工写进基线并说明为什么
 * 语义上确实更强。表外的替换（含跨家族、含 ReadActor 方向）一律判红。
 * 码集合的变化不在这里判 —— 那由「差集」规则管（新码没声明就红），两件事分开。
 *
 * 三种情况：
 *   - **允许升级**：基线守卫 + 新守卫 命中 `允许升级对` → 记 note + `守卫升级记录`。
 *   - **基线时即不产出状态码**：那个守卫当时就被 `x, _ :=` / `(Actor, bool)` 吞掉了，
 *     换掉它没有丢任何可观测行为（例如 Forum.commentPage 的 `viewer, _ := LoadActor`
 *     改成 OptionalActor）→ 记 note。这条也不看表：它比较的是「有没有码逃出去」，
 *     不是「守卫身份换没换」。
 *   - **其它**（改名、换成直连、降级、跨家族）：判红。
 */
function checkEndpointGuards(observation, baseline, ctx) {
  const allowedPairs = new Set(baseline.upgradePairs.map((pair) => `${pair.from} -> ${pair.to}`));
  let checked = 0;
  for (const [key, expected] of baseline.endpoints) {
    const current = observation.endpoints.get(key) || [];
    const missing = expected.filter((guard) => !current.includes(guard));
    if (missing.length === 0) {
      checked += 1;
      continue;
    }
    const added = current.filter((guard) => !expected.includes(guard));
    const producedAtBaseline = new Set(baseline.codeEndpoints.get(key) || []);
    const used = new Set();
    const unexplained = [];
    for (const gone of missing) {
      // 基线时就被吞掉（`x, _ :=` / `(Actor, bool)`）的守卫本来一个码都不产出，
      // 换掉它没有丢任何东西 —— 例如 Forum.commentPage 的 `viewer, _ := LoadActor`
      // 改成 OptionalActor。这不是回退，只记 note。
      if (!producedAtBaseline.has(gone)) {
        ctx.note(`端点守卫替换（基线时即不产出状态码）：${key} 去掉 ${gone}，实测=[${current.join(', ') || '(无)'}]`);
        continue;
      }
      const upgrade = added.find(
        (candidate) => !used.has(candidate) && allowedPairs.has(`${gone} -> ${candidate}`),
      );
      if (!upgrade) {
        unexplained.push(gone);
        continue;
      }
      used.add(upgrade);
      ctx.upgrades.push({
        operation: key,
        from: gone,
        to: upgrade,
        fromCodes: guardCodesOf(gone),
        toCodes: guardCodesOf(upgrade),
      });
    }
    if (unexplained.length > 0) {
      ctx.fail(
        'guard-removed',
        `端点守卫消失：${key} 基线=[${expected.join(', ')}] 实测=[${current.join(', ') || '(无)'}] ` +
          `消失=${unexplained.join(', ')} —— 替换不在基线「允许升级对」里（改名/换成直连/降级/跨家族）。` +
          '确实语义更强的话，请把这一对连同中文原因写进基线「允许升级对」，不要靠运行时推导',
      );
    }
    checked += 1;
  }
  // 升级是**允许**的变更，不是失败项；但日志要能读出规模。超过 40 条时只打印前 40 条，
  // 其余靠 --refresh 落进「守卫升级记录」供审计，避免一次重构把 CI 日志刷成 100+ 行。
  const NOTE_LIMIT = 40;
  for (const upgrade of ctx.upgrades.slice(0, NOTE_LIMIT)) {
    ctx.note(
      `端点守卫升级（基线「允许升级对」已登记）：${upgrade.operation} ` +
        `${upgrade.from}[${upgrade.fromCodes.join(',')}] → ${upgrade.to}[${upgrade.toCodes.join(',')}]` +
        '（--refresh 会写入「守卫升级记录」）',
    );
  }
  if (ctx.upgrades.length > NOTE_LIMIT) {
    ctx.note(`另有 ${ctx.upgrades.length - NOTE_LIMIT} 条同类守卫升级未逐条打印（共 ${ctx.upgrades.length} 条，全部会写入「守卫升级记录」）`);
  }
  return checked;
}

/** 通配/ALL 路由必须逐条登记：新增一条 /* 就可能吞掉一批已映射的 operation。 */
function checkWildcard(observation, baseline, ctx) {
  const allowed = new Set(baseline.wildcard.map((entry) => entry.operation));
  for (const entry of observation.wildcard) {
    if (!allowed.has(entry.operation)) {
      ctx.fail(
        'wildcard-unregistered',
        `未登记的通配/ALL 路由：${entry.operation} handler=${entry.handler} [${entry.file}] ` +
          '—— 通配路由会遮蔽已映射 operation，必须逐条登记原因',
      );
    }
  }
  for (const operation of allowed) {
    if (!observation.wildcard.some((entry) => entry.operation === operation)) {
      ctx.note(`基线「通配路由」已过期：${operation} 现在不是通配路由，可跑 --refresh 移除`);
    }
  }
}

/** 差集：guard 可达码未声明 → 必须逐条登记；登记过的码不许再多出没登记的。 */
function checkGaps(observation, baseline, ctx) {
  const recorded = new Map(baseline.gaps.map((entry) => [entry.operation, entry]));
  const exempt = new Map(baseline.falsePositives.map((entry) => [entry.operation, entry]));
  let checked = 0;

  for (const gap of observation.gaps) {
    const entry = recorded.get(gap.operation);
    const waived = exempt.get(gap.operation);
    const covered = new Set([...(entry?.codes || []), ...(waived?.codes || [])]);
    const unrecorded = gap.codes.filter((code) => !covered.has(code));
    if (unrecorded.length === 0) {
      checked += 1;
      continue;
    }
    ctx.fail(
      'gap-unregistered',
      `路由可达但契约未声明的状态码未登记：${gap.operation} 多出=${unrecorded.join(',')} ` +
        `（基线已登记=[${[...covered].join(',') || '无'}]）—— 要么补契约，要么写进基线并说明原因`,
    );
  }

  for (const [operation, entry] of recorded) {
    const stillGap = observation.gaps.find((gap) => gap.operation === operation);
    if (!stillGap) {
      ctx.note(`基线「已登记未声明状态码」已过期：${operation}（${entry.codes.join(',')}）现在不再缺，可跑 --refresh 移除`);
      continue;
    }
    const surplus = entry.codes.filter((code) => !stillGap.codes.includes(code));
    if (surplus.length > 0) {
      ctx.note(`基线「已登记未声明状态码」有多余码：${operation} 基线=${entry.codes.join(',')} 实测=${stillGap.codes.join(',')}`);
    }
  }
  return checked;
}

/** 退化区：新条目进入 = 又一条 operation 掉进「∅ ⊆ 任意」，必须红。 */
function checkDegenerate(observation, baseline, ctx) {
  const recorded = new Set(baseline.degenerate.map((entry) => entry.operation));
  for (const entry of observation.degenerate) {
    if (recorded.has(entry.operation)) continue;
    ctx.fail(
      'degenerate-new',
      `新的退化区 operation：${entry.operation} handler=${entry.handler} ` +
        `guards=[${entry.guards.join(',') || '无'}] —— parity 门对它退化成 ∅ ⊆ 任意，必须登记或补契约`,
    );
  }
  const current = new Set(observation.degenerate.map((entry) => entry.operation));
  const left = [...recorded].filter((operation) => !current.has(operation)).length;
  if (left > 0) ctx.note(`退化区有 ${left} 条已离开（好转），可跑 --refresh 收紧清单`);
}

/**
 * 反盲区：本门不认识的、自己直接产出 401/503 的函数必须**具名**登记。
 *
 * 只钉计数等于「删一个加一个仍然绿」，没人能审计。所以登记 id + codes + 中文原因，
 * 与另三组例外同构：新增一个未登记的名字即红。
 */
function checkUnregisteredProducers(observation, baseline, ctx) {
  const recorded = new Map(baseline.unregistered.map((entry) => [entry.id, entry]));
  for (const producer of observation.unregisteredGates) {
    const entry = recorded.get(producer.id);
    if (!entry) {
      ctx.fail(
        'unregistered-producer-new',
        `新的未登记 401/503 产出点：${producer.id} 产出=${producer.codes.join(',')} ` +
          '—— 它自己直接答 401/503，却不在 guard 登记表里；要么登记为 guard，要么写进基线并说明为什么不算门',
      );
      continue;
    }
    const surplus = producer.codes.filter((code) => !entry.codes.includes(code));
    if (surplus.length > 0) {
      ctx.fail(
        'unregistered-producer-new',
        `未登记 401/503 产出点产出变多：${producer.id} 基线=${entry.codes.join(',')} 实测=${producer.codes.join(',')}`,
      );
    }
  }
  const current = new Set(observation.unregisteredGates.map((producer) => producer.id));
  const stale = baseline.unregistered.filter((entry) => !current.has(entry.id)).length;
  if (stale > 0) ctx.note(`基线「未登记401503产出点」有 ${stale} 条已消失，可跑 --refresh 移除`);
}

/**
 * 跑完所有规则，返回本次核对到的条数。
 *
 * ctx 由调用方提供（`fail(rule, message)` / `note(message)` / `upgrades` 三个收集器），
 * 所以这一层不需要知道 stdout/stderr 长什么样。
 */
function evaluateRules({ observation, scan, baseline, ctx, root, repoRoot }) {
  const registryIds = new Set(GUARD_REGISTRY.map((entry) => `${entry.dir}:${entry.name}`));
  if (registryIds.size !== GUARD_REGISTRY.length) {
    ctx.fail('registry-stale', 'GUARD_REGISTRY 里有重复条目，登记表自检失败');
  }
  // 包目录不存在 = fixture 树里没有这个控制器包（合成树只带被测的那几个包）。
  // 仓库自身永远有这 6 个目录，所以这条豁免在真实仓库里不会触发；万一整个目录被删，
  // 「实现侧路由数」计数与「端点守卫消失」两条规则会同时红。
  const isFixtureTree = root !== repoRoot;
  for (const entry of scan.registryPresence) {
    if (entry.present) continue;
    if (isFixtureTree && entry.reason === '包目录不存在') continue;
    ctx.fail(
      'registry-stale',
      `guard 白名单条目过期：${entry.id} 在实现侧找不到函数体（${entry.reason}）。` +
        '改名/删除后必须同步 GUARD_REGISTRY，否则该守卫的状态码静默消失、差集永远变绿',
    );
  }

  checkCounts(observation, baseline, ctx);
  const guardChecked = checkEndpointGuards(observation, baseline, ctx);
  checkWildcard(observation, baseline, ctx);
  const gapChecked = checkGaps(observation, baseline, ctx);
  checkDegenerate(observation, baseline, ctx);
  checkUnregisteredProducers(observation, baseline, ctx);
  return { guardChecked, gapChecked };
}

export { evaluateRules, checkEndpointGuards };

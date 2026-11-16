/**
 * 输出格式化层：把判定结果拼成给人看的行。
 *
 * 只做字符串拼接，不读文件、不判定、不改退出码。等价性测试与 fixture 测试都靠解析
 * 这里的输出行工作，所以格式一旦变了，测试会立刻红 —— 这是有意的耦合。
 */

/** 汇总块：分母、guard 分布、退化区、差集、产出点。 */
function renderSummary({
  observation,
  scan,
  baseline,
  guardRegistrySize,
  presentCount,
  guardChecked,
  gapChecked,
}) {
  const distribution = new Map();
  for (const guards of observation.endpoints.values()) {
    for (const guard of guards) distribution.set(guard, (distribution.get(guard) || 0) + 1);
  }
  return [
    '[reach] 契约可达性门（路由 → 同包调用图 → guard 助手）',
    `[reach] 契约 operation 数：${observation.counts.契约operation数}（path item ${scan.contractPathCount} 个）`,
    `[reach] 实现侧路由数：${observation.counts.实现侧路由数}` +
      `（其中契约无对应 operation 的 ${observation.implementationOnly} 条不计入 guard 比对）`,
    `[reach] guard 助手登记：${guardRegistrySize} 条，实际命中 ${presentCount} 条`,
    `[reach] 端点 guard 分布：${[...distribution.entries()]
      .sort((a, b) => b[1] - a[1])
      .map(([guard, count]) => `${guard}=${count}`)
      .join('  ')}`,
    `[reach] 退化区（parity 门状态码集合为空）：${observation.counts.退化区路由数} 条路由 / ` +
      `${observation.counts.退化区已映射operation数} 条已映射 operation`,
    `[reach] 路由可达 401/503 但契约未声明：${observation.counts.路由可达guard码但契约未声明条数} 条` +
      `（基线登记 ${baseline.gaps.length} 条、已复核误报 ${baseline.falsePositives.length} 条，本次核对 ${gapChecked} 条）`,
    `[reach] 端点守卫核对：${guardChecked} 条（基线 ${baseline.endpoints.size} 条）` +
      `；通配/ALL 路由：${observation.wildcard.length} 条（基线 ${baseline.wildcard.length} 条）`,
    `[reach] 未登记 401/503 产出点：${observation.counts.未登记401503产出点数} 个` +
      `（基线具名登记 ${baseline.unregistered.length} 个）`,
  ];
}

function renderNotes(notes) {
  return notes.map((message) => `[reach] 提示：${message}`);
}

/** 失败块。`footer` 用来放「拒绝写基线」这类收尾说明。 */
function renderFailures(failures, footer) {
  const lines = [`\n[reach] 失败 ${failures.length} 项：`];
  for (const failure of failures) lines.push(`  ✗ [${failure.rule}] ${failure.message}`);
  if (footer) lines.push(footer);
  return lines;
}

/** --print-manifest 在扫描期已有失败时：清单照打，但要明确告诉 W3 它可能缺条目。 */
function renderManifestIncomplete(failures) {
  const lines = [`\n[reach] 清单不完整：扫描期有 ${failures.length} 项失败，清单可能缺条目`];
  for (const failure of failures) lines.push(`  ✗ [${failure.rule}] ${failure.message}`);
  return lines;
}

export { renderSummary, renderNotes, renderFailures, renderManifestIncomplete };

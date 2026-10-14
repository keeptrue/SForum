import { describe, expect, test } from 'bun:test'

import { normalizedAISettingsForComparison, promptUsesDefault, toolCallsPerReplyUsesDefault } from '../../app/utils/admin/adminAI'

const DEFAULT_PROMPT = '你是助手。\n\n要求：\n- 简短\n- 用中文'

describe('admin AI prompt comparison', () => {
  test('treats empty and default-equal prompts as using the default', () => {
    expect(promptUsesDefault('', DEFAULT_PROMPT)).toBe(true)
    expect(promptUsesDefault(undefined, DEFAULT_PROMPT)).toBe(true)
    expect(promptUsesDefault(DEFAULT_PROMPT, DEFAULT_PROMPT)).toBe(true)
  })

  test('ignores whitespace differences the editor introduces', () => {
    // 编辑器会在段落与列表之间补空行、去掉行尾空白：这些不算自定义。
    const editorRoundTrip = `${DEFAULT_PROMPT.replace('\n- ', '\n\n- ')}\n`
    expect(promptUsesDefault(editorRoundTrip, DEFAULT_PROMPT)).toBe(true)
  })

  test('keeps a real customization distinguishable', () => {
    expect(promptUsesDefault('你是助手。\n\n要求：\n- 简短', DEFAULT_PROMPT)).toBe(false)
    expect(promptUsesDefault('完全不同的提示词', DEFAULT_PROMPT)).toBe(false)
  })

  test('canonicalizes default prompts so empty-vs-omitted compares equal', () => {
    // 服务端在提示词为空时省略字段，表单侧可能持有显式空串。
    const stored = { reply: {}, gates: { toolCallsPerReply: 3 } }
    const form = { reply: { systemPrompt: '' }, gates: { toolCallsPerReply: 3 } }
    expect(JSON.stringify(normalizedAISettingsForComparison(form, DEFAULT_PROMPT)))
      .toBe(JSON.stringify(normalizedAISettingsForComparison(stored, DEFAULT_PROMPT)))
  })

  test('does not collapse a customized prompt into the default', () => {
    const stored = { reply: {} }
    const customized = { reply: { systemPrompt: `${DEFAULT_PROMPT}\n- 另外补充一句` } }
    expect(JSON.stringify(normalizedAISettingsForComparison(customized, DEFAULT_PROMPT)))
      .not.toBe(JSON.stringify(normalizedAISettingsForComparison(stored, DEFAULT_PROMPT)))
  })

  test('treats missing, empty, unknown and explicit mention trigger modes as equal', () => {
    // 服务端对未知值 / 空串归一为缺省（mention），且缺省时字段可能整个省略。
    const stored = { reply: {} }
    const empty = { reply: { triggerMode: '' } }
    const unknown = { reply: { triggerMode: 'yolo' } }
    const explicit = { reply: { triggerMode: 'mention' } }
    const canonical = JSON.stringify(normalizedAISettingsForComparison(stored, DEFAULT_PROMPT))
    expect(JSON.stringify(normalizedAISettingsForComparison(empty, DEFAULT_PROMPT))).toBe(canonical)
    expect(JSON.stringify(normalizedAISettingsForComparison(unknown, DEFAULT_PROMPT))).toBe(canonical)
    expect(JSON.stringify(normalizedAISettingsForComparison(explicit, DEFAULT_PROMPT))).toBe(canonical)
  })

  test('keeps an autonomous or off trigger mode distinguishable from mention', () => {
    const stored = { reply: {} }
    for (const triggerMode of ['autonomous', 'off']) {
      expect(JSON.stringify(normalizedAISettingsForComparison({ reply: { triggerMode } }, DEFAULT_PROMPT)))
        .not.toBe(JSON.stringify(normalizedAISettingsForComparison(stored, DEFAULT_PROMPT)))
    }
  })
})

describe('admin AI tool call budget default semantics', () => {
  test('treats missing and null as unset', () => {
    expect(toolCallsPerReplyUsesDefault(undefined)).toBe(true)
    expect(toolCallsPerReplyUsesDefault(null)).toBe(true)
    expect(toolCallsPerReplyUsesDefault(Number.NaN)).toBe(true)
  })

  test('distinguishes an explicit zero from unset', () => {
    // 0 是「停用工具调用」，与「未设置按默认 3 生效」是两种语义。
    expect(toolCallsPerReplyUsesDefault(0)).toBe(false)
    expect(toolCallsPerReplyUsesDefault(3)).toBe(false)
  })

  test('drops unset tool calls so stored nil compares equal to form undefined', () => {
    const stored = { gates: {} }
    const form = { gates: { toolCallsPerReply: undefined } }
    expect(JSON.stringify(normalizedAISettingsForComparison(form, DEFAULT_PROMPT)))
      .toBe(JSON.stringify(normalizedAISettingsForComparison(stored, DEFAULT_PROMPT)))
  })

  test('keeps an explicit value from being collapsed into the default', () => {
    // 显式 3 与未设置不同：前者会固化，后者会跟随后端默认值的调整。
    const unset = { gates: {} }
    const explicit = { gates: { toolCallsPerReply: 3 } }
    expect(JSON.stringify(normalizedAISettingsForComparison(explicit, DEFAULT_PROMPT)))
      .not.toBe(JSON.stringify(normalizedAISettingsForComparison(unset, DEFAULT_PROMPT)))
  })

  test('normalizes a numeric string to a number', () => {
    expect(normalizedAISettingsForComparison({ gates: { toolCallsPerReply: '3' as never } }, DEFAULT_PROMPT).gates)
      .toEqual({ toolCallsPerReply: 3 })
  })
})

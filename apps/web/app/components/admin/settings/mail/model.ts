export type MailProviderState = 'ready' | 'unavailable' | 'unconfigured'
export type MailProvider = { extensionId: string, label: string, healthy: boolean, status?: 'enabled' | 'unavailable' }
export type MailProviderResponse = {
  items: MailProvider[]
  selected: { extensionId: string, label: string }
  configured: boolean
  state: MailProviderState
}
export type MailDelivery = { id: number, recipient: string, templateKey: string, status: string, reason?: string, errorSummary?: string, createdAt: string }
export type MailChannelPolicy = { inAppEnabled: boolean, emailEnabled: boolean }
export type MailPolicy = { reply: MailChannelPolicy, mention: MailChannelPolicy, moderation: MailChannelPolicy }

export const recommendedMailPolicy = (): MailPolicy => ({
  reply: { inAppEnabled: true, emailEnabled: true },
  mention: { inAppEnabled: true, emailEnabled: true },
  moderation: { inAppEnabled: true, emailEnabled: true }
})

export function mailDeliveryCodeKey(group: 'deliveryStatus' | 'templates' | 'reasons', code: string) {
  return `admin.mailSettings.${group}.${code.replaceAll('.', '_')}`
}

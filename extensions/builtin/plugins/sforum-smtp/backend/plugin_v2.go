package main

import (
	"context"
	"os"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 与 Host protocol_v2_client.providerCall 对齐的 known-slot schema。
const (
	smtpMailSlot              = "mail.provider"
	smtpProbeOutputSchema     = "sforum.provider.mail.provider.probe.response@1"
	smtpSendOutputSchema      = "sforum.provider.mail.provider.send.response@1"
	smtpLegacyContractVersion = "1"
)

// smtpPluginV2 覆盖 ProviderCall 以承接 Host 遗留 known-slot probe/send。
// 不得经 typed ProviderRegistry 伪装 probe/send（仅 invoke）。
type smtpPluginV2 struct {
	*pluginv2.Server
}

func newSMTPPluginV2() *smtpPluginV2 {
	return &smtpPluginV2{Server: pluginv2.NewServer()}
}

func (p *smtpPluginV2) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(),
			Extension: request.GetContext().GetExtension(),
		},
	}
	// 复用 Server 的精确制品握手校验，避免绕过 stale-runtime 检查。
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.GetSlotId() != smtpMailSlot {
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			Reason:  "provider.slot_unsupported",
			Message: "unsupported provider slot",
		}
		return response, nil
	}
	if request.GetContractVersion() != "" && request.GetContractVersion() != smtpLegacyContractVersion {
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "provider.contract_mismatch",
			Message: "mail.provider contract must be version 1",
		}
		return response, nil
	}

	config := loadSMTPConfigFromEnv()
	switch request.GetOperation() {
	case "probe":
		return p.probe(response, config)
	case "send":
		return p.send(response, config, request)
	default:
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "provider.operation_unsupported",
			Message: "mail.provider accepts only probe and send operations",
		}
		return response, nil
	}
}

func (p *smtpPluginV2) probe(response *pluginwire.ProviderCallResponse, config smtpConfig) (*pluginwire.ProviderCallResponse, error) {
	if err := probeSMTP(config); err != nil {
		output, docErr := pluginv2.NewTypedDocument(smtpProbeOutputSchema, map[string]any{
			"ok": false, "reason": err.reason, "message": err.message,
		})
		if docErr != nil {
			return nil, docErr
		}
		response.Output = output
		return response, nil
	}
	output, err := pluginv2.NewTypedDocument(smtpProbeOutputSchema, map[string]any{
		"ok": true, "reason": "smtp.connection_ok",
		"message": "SMTP connection and authentication succeeded.",
	})
	if err != nil {
		return nil, err
	}
	response.Output = output
	return response, nil
}

func (p *smtpPluginV2) send(
	response *pluginwire.ProviderCallResponse,
	config smtpConfig,
	request *pluginwire.ProviderCallRequest,
) (*pluginwire.ProviderCallResponse, error) {
	values := pluginv2.TypedDocumentValues(request.GetInput())
	mailRequest := mailRequest{
		DeliveryID: stringValue(values, "deliveryId"), CorrelationID: stringValue(values, "correlationId"),
		FromAddress: stringValue(values, "fromAddress"), FromName: stringValue(values, "fromName"),
		To: stringSliceValue(values, "to"), Subject: stringValue(values, "subject"),
		TextBody: stringValue(values, "textBody"), HTMLBody: stringValue(values, "htmlBody"),
	}
	if err := sendSMTP(config, mailRequest); err != nil {
		output, docErr := pluginv2.NewTypedDocument(smtpSendOutputSchema, map[string]any{
			"ok": false, "classification": err.classification,
			"reason": err.reason, "message": err.message,
		})
		if docErr != nil {
			return nil, docErr
		}
		response.Output = output
		return response, nil
	}
	output, err := pluginv2.NewTypedDocument(smtpSendOutputSchema, map[string]any{
		"ok": true, "reason": "smtp.sent",
	})
	if err != nil {
		return nil, err
	}
	response.Output = output
	return response, nil
}

func loadSMTPConfigFromEnv() smtpConfig {
	port, _ := strconv.Atoi(os.Getenv("SFORUM_SETTING_PORT"))
	return smtpConfig{
		Host: os.Getenv("SFORUM_SETTING_HOST"), Port: port,
		Encryption:  os.Getenv("SFORUM_SETTING_ENCRYPTION"),
		Username:    os.Getenv("SFORUM_SETTING_USERNAME"),
		Password:    os.Getenv("SFORUM_SETTING_PASSWORD"),
		FromAddress: os.Getenv("SFORUM_SETTING_FROM_ADDRESS"),
		FromName:    os.Getenv("SFORUM_SETTING_FROM_NAME"),
	}
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch raw := values[key].(type) {
	case string:
		return raw
	case float64:
		return strconv.FormatInt(int64(raw), 10)
	case int64:
		return strconv.FormatInt(raw, 10)
	default:
		return ""
	}
}

func stringSliceValue(values map[string]any, key string) []string {
	if values == nil {
		return nil
	}
	raw, ok := values[key]
	if !ok || raw == nil {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

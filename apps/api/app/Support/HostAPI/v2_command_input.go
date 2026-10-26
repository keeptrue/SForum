package hostapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func decodeProtocolV2CommandInput[T any](request *hostv2.CommandRequest) (T, error) {
	var result T
	if request == nil || request.GetInput() == nil || request.GetInput().GetValue() == nil {
		return result, invalidProtocolV2DomainCommandInput()
	}
	body, err := request.GetInput().GetValue().MarshalJSON()
	if err != nil {
		return result, invalidProtocolV2DomainCommandInput()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, invalidProtocolV2DomainCommandInput()
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return result, invalidProtocolV2DomainCommandInput()
	}
	return result, nil
}

func invalidProtocolV2DomainCommandInput() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
		"host.command_input_invalid",
		"The Host Command input is invalid.",
		false,
	)
}

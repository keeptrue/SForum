package ai_test

import (
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

// imageRequest 构造一条带图片的用户消息：契约允许的形态是文本 + 图片混合
// parts，图片以 base64 内联。
func imageRequest() ai.CompletionRequest {
	request := validRequest()
	request.Messages = []ai.Message{{
		Role: ai.RoleUser,
		Parts: []ai.Part{
			{Type: ai.PartText, Text: "这张图里报错是什么意思？"},
			{Type: ai.PartImage, Image: &ai.ImageContent{MediaType: "image/png", Data: "aW1hZ2UtYnl0ZXM="}},
		},
	}}
	return request
}

func TestOpenAIRequestSerializesImageAsContentArray(t *testing.T) {
	wire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", imageRequest())
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Messages) != 2 { // system + user
		t.Fatalf("expected 2 messages, got %d", len(payload.Messages))
	}
	user := payload.Messages[1]
	if !strings.HasPrefix(string(user.Content), "[") {
		t.Fatalf("user content with images must be an array, got %s", string(user.Content))
	}
	var parts []map[string]any
	if err := json.Unmarshal(user.Content, &parts); err != nil {
		t.Fatalf("unmarshal content parts: %v", err)
	}
	if len(parts) != 2 || parts[0]["type"] != "text" || parts[1]["type"] != "image_url" {
		t.Fatalf("unexpected parts: %+v", parts)
	}
	imageURL, _ := parts[1]["image_url"].(map[string]any)
	if imageURL["url"] != "data:image/png;base64,aW1hZ2UtYnl0ZXM=" {
		t.Fatalf("unexpected image url: %+v", imageURL)
	}
}

func TestOpenAIRequestKeepsPlainTextContentAsString(t *testing.T) {
	wire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", validRequest())
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	// 无图片的消息必须保持字符串 content——这是兼容面最大的形态。
	for _, message := range payload.Messages {
		if strings.HasPrefix(string(message.Content), "[") {
			t.Fatalf("text-only message must keep string content, got %s", string(message.Content))
		}
	}
}

func TestAnthropicRequestSerializesImageBlocks(t *testing.T) {
	profile := deepseekProfile()
	profile.Protocol = ai.ProtocolAnthropicMessages
	wire, err := ai.BuildWireRequest(profile, "sk-test", imageRequest())
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	var payload struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(payload.Messages))
	}
	content := payload.Messages[0].Content
	if len(content) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(content))
	}
	if content[0].Type != "text" || content[0].Text != "这张图里报错是什么意思？" {
		t.Fatalf("unexpected text block: %+v", content[0])
	}
	if content[1].Type != "image" || content[1].Source == nil {
		t.Fatalf("unexpected image block: %+v", content[1])
	}
	if content[1].Source.Type != "base64" || content[1].Source.MediaType != "image/png" || content[1].Source.Data != "aW1hZ2UtYnl0ZXM=" {
		t.Fatalf("unexpected image source: %+v", content[1].Source)
	}
}

func TestValidateRejectsInvalidImageParts(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ai.CompletionRequest)
		wantErr bool
	}{
		{
			name: "image on assistant role is rejected",
			mutate: func(r *ai.CompletionRequest) {
				r.Messages = []ai.Message{{
					Role: ai.RoleAssistant,
					Parts: []ai.Part{
						{Type: ai.PartText, Text: "hi"},
						{Type: ai.PartImage, Image: &ai.ImageContent{MediaType: "image/png", Data: "aW1hZ2U="}},
					},
				}}
			},
			wantErr: true,
		},
		{
			name: "unsupported media type is rejected",
			mutate: func(r *ai.CompletionRequest) {
				r.Messages = []ai.Message{{
					Role: ai.RoleUser,
					Parts: []ai.Part{
						{Type: ai.PartText, Text: "hi"},
						{Type: ai.PartImage, Image: &ai.ImageContent{MediaType: "image/svg+xml", Data: "aW1hZ2U="}},
					},
				}}
			},
			wantErr: true,
		},
		{
			name: "nil image content is rejected",
			mutate: func(r *ai.CompletionRequest) {
				r.Messages = []ai.Message{{
					Role: ai.RoleUser,
					Parts: []ai.Part{
						{Type: ai.PartText, Text: "hi"},
						{Type: ai.PartImage},
					},
				}}
			},
			wantErr: true,
		},
		{
			name: "empty image data is rejected",
			mutate: func(r *ai.CompletionRequest) {
				r.Messages = []ai.Message{{
					Role: ai.RoleUser,
					Parts: []ai.Part{
						{Type: ai.PartText, Text: "hi"},
						{Type: ai.PartImage, Image: &ai.ImageContent{MediaType: "image/png", Data: ""}},
					},
				}}
			},
			wantErr: true,
		},
		{
			name: "valid image on user message passes",
			mutate: func(r *ai.CompletionRequest) {
				*r = imageRequest()
			},
			wantErr: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := validRequest()
			testCase.mutate(&request)
			err := request.Validate()
			if (err != nil) != testCase.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestValidateRejectsTooManyImages(t *testing.T) {
	request := validRequest()
	parts := []ai.Part{{Type: ai.PartText, Text: "看这些图"}}
	for i := 0; i < ai.MaxImagesPerMessage+1; i++ {
		parts = append(parts, ai.Part{Type: ai.PartImage, Image: &ai.ImageContent{MediaType: "image/png", Data: "aW1hZ2U="}})
	}
	request.Messages = []ai.Message{{Role: ai.RoleUser, Parts: parts}}
	if err := request.Validate(); err == nil {
		t.Fatal("expected error for exceeding MaxImagesPerMessage")
	}
}

func TestValidImageMediaType(t *testing.T) {
	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif"} {
		if !ai.ValidImageMediaType(mediaType) {
			t.Fatalf("expected %s to be valid", mediaType)
		}
	}
	for _, mediaType := range []string{"image/svg+xml", "video/mp4", "text/plain", ""} {
		if ai.ValidImageMediaType(mediaType) {
			t.Fatalf("expected %s to be invalid", mediaType)
		}
	}
}

package vertex

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/converter/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestSizeToAspectRatio(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{"1024x1024", "1:1"},
		{"512x512", "1:1"},
		{"256x256", "1:1"},
		{"1792x1024", "16:9"},
		{"1024x1792", "9:16"},
		{"1536x1024", "3:2"},
		{"1024x1536", "2:3"},
		{"768x1024", "3:4"},
		{"1024x768", "4:3"},
		{"819x1024", "4:5"},
		{"1024x819", "5:4"},
		{"576x1024", "9:16"},
		{"2016x1008", "21:9"},
		{"unknown", "1:1"}, // default
		{"", "1:1"},        // default
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			assert.Equal(t, tt.want, sizeToAspectRatio(tt.size))
		})
	}
}

func TestSizeToImageSize(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		// 1K sizes
		{"1024x1024", "1K"},
		{"512x512", "1K"},
		{"256x256", "1K"},
		{"1792x1024", "1K"},
		{"1024x1792", "1K"},
		{"576x1024", "1K"},
		// 2K sizes
		{"2048x2048", "2K"},
		{"3584x2048", "2K"},
		{"2048x3584", "2K"},
		{"2016x1008", "2K"},
		// 4K sizes
		{"4096x4096", "4K"},
		{"7168x4096", "4K"},
		{"4096x7168", "4K"},
		// Unknown → 1K default
		{"unknown", "1K"},
		{"", "1K"},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			assert.Equal(t, tt.want, sizeToImageSize(tt.size))
		})
	}
}

func TestImageRequestToOpenAIChatRequest(t *testing.T) {
	t.Run("basic request with prompt and model", func(t *testing.T) {
		input := `{"model": "gemini-2.0-flash", "prompt": "A cat sitting on a mat"}`
		result, err := ImageRequestToOpenAIChatRequest([]byte(input))
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		err = json.Unmarshal(result, &chatReq)
		require.NoError(t, err)

		assert.Equal(t, "gemini-2.0-flash", chatReq.Model)
		require.Len(t, chatReq.Messages, 1)
		assert.Equal(t, "user", chatReq.Messages[0].Role)
		assert.Equal(t, "A cat sitting on a mat", chatReq.Messages[0].Content)

		// Check extra_body has generation_config with IMAGE modality
		require.NotNil(t, chatReq.ExtraBody)
		genConfig, ok := chatReq.ExtraBody["generation_config"].(map[string]interface{})
		require.True(t, ok)
		modalities, ok := genConfig["response_modalities"].([]interface{})
		require.True(t, ok)
		assert.Contains(t, modalities, "IMAGE")
	})

	t.Run("request with size includes aspect ratio and image size", func(t *testing.T) {
		input := `{"model": "gemini-2.0-flash", "prompt": "A landscape", "size": "1792x1024"}`
		result, err := ImageRequestToOpenAIChatRequest([]byte(input))
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		err = json.Unmarshal(result, &chatReq)
		require.NoError(t, err)

		genConfig, ok := chatReq.ExtraBody["generation_config"].(map[string]interface{})
		require.True(t, ok)
		imageConfig, ok := genConfig["image_config"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "16:9", imageConfig["aspectRatio"])
		assert.Equal(t, "1K", imageConfig["imageSize"])
	})

	t.Run("request clamps n to 10", func(t *testing.T) {
		input := `{"model":"gemini-2.0-flash","prompt":"A landscape","n":99}`
		result, err := ImageRequestToOpenAIChatRequest([]byte(input))
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		require.NoError(t, json.Unmarshal(result, &chatReq))
		require.NotNil(t, chatReq.N)
		assert.Equal(t, 10, *chatReq.N)
	})

	t.Run("invalid JSON input returns error", func(t *testing.T) {
		result, err := ImageRequestToOpenAIChatRequest([]byte("not json"))
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to parse OpenAI image request")
	})
}

func TestImageEditRequestToOpenAIChatRequest(t *testing.T) {
	buildMultipart := func(t *testing.T) ([]byte, string) {
		t.Helper()

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", "gemini-2.5-flash-image-preview"))
		require.NoError(t, writer.WriteField("prompt", "Make the object blue"))
		require.NoError(t, writer.WriteField("n", "2"))
		require.NoError(t, writer.WriteField("size", "1792x1024"))

		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": []string{`form-data; name="image"; filename="input.png"`},
			"Content-Type":        []string{"image/png"},
		})
		require.NoError(t, err)
		_, err = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		require.NoError(t, err)

		require.NoError(t, writer.Close())
		return buf.Bytes(), writer.FormDataContentType()
	}

	t.Run("multipart edit request converts to multimodal chat request", func(t *testing.T) {
		body, contentType := buildMultipart(t)
		result, err := ImageEditRequestToOpenAIChatRequest(body, contentType)
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		require.NoError(t, json.Unmarshal(result, &chatReq))
		assert.Equal(t, "gemini-2.5-flash-image-preview", chatReq.Model)
		require.NotNil(t, chatReq.N)
		assert.Equal(t, 2, *chatReq.N)
		require.Len(t, chatReq.Messages, 1)

		blocks, ok := chatReq.Messages[0].Content.([]interface{})
		require.True(t, ok)
		require.Len(t, blocks, 2)

		textBlock, ok := blocks[0].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "text", textBlock["type"])
		assert.Equal(t, "Make the object blue", textBlock["text"])

		imageBlock, ok := blocks[1].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "image_url", imageBlock["type"])

		genConfig, ok := chatReq.ExtraBody["generation_config"].(map[string]interface{})
		require.True(t, ok)
		imageConfig, ok := genConfig["image_config"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "16:9", imageConfig["aspectRatio"])
		assert.Equal(t, "1K", imageConfig["imageSize"])
	})

	t.Run("missing model returns error", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("prompt", "Edit this"))
		require.NoError(t, writer.Close())

		result, err := ImageEditRequestToOpenAIChatRequest(buf.Bytes(), writer.FormDataContentType())
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "missing model")
	})

	t.Run("mask image is included in content blocks", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", "gemini-2.5-flash-image-preview"))
		require.NoError(t, writer.WriteField("prompt", "Edit with mask"))

		imagePart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": []string{`form-data; name="image"; filename="input.png"`},
			"Content-Type":        []string{"image/png"},
		})
		require.NoError(t, err)
		_, err = imagePart.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		require.NoError(t, err)

		maskPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": []string{`form-data; name="mask"; filename="mask.png"`},
			"Content-Type":        []string{"image/png"},
		})
		require.NoError(t, err)
		_, err = maskPart.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
		require.NoError(t, err)

		require.NoError(t, writer.Close())

		result, err := ImageEditRequestToOpenAIChatRequest(buf.Bytes(), writer.FormDataContentType())
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		require.NoError(t, json.Unmarshal(result, &chatReq))
		blocks, ok := chatReq.Messages[0].Content.([]interface{})
		require.True(t, ok)
		require.Len(t, blocks, 4)

		maskPrompt, ok := blocks[2].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "text", maskPrompt["type"])
		assert.Contains(t, maskPrompt["text"], "mask")

		maskBlock, ok := blocks[3].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "image_url", maskBlock["type"])
	})

	t.Run("non-image mime type returns error", func(t *testing.T) {
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", "gemini-2.5-flash-image-preview"))
		require.NoError(t, writer.WriteField("prompt", "Edit this"))

		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Disposition": []string{`form-data; name="image"; filename="input.txt"`},
			"Content-Type":        []string{"text/plain"},
		})
		require.NoError(t, err)
		_, err = part.Write([]byte("not an image"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		result, err := ImageEditRequestToOpenAIChatRequest(buf.Bytes(), writer.FormDataContentType())
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "unsupported MIME type")
	})
}

// ---------------------------------------------------------------------------
// convertVertexUsageToImageUsage
// ---------------------------------------------------------------------------

func TestConvertVertexUsageToImageUsage(t *testing.T) {
	t.Run("basic prompt and candidate counts, no modality details", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     268,
			CandidatesTokenCount: 1024,
		}
		got := convertVertexUsageToImageUsage(meta)
		require.NotNil(t, got)
		assert.Equal(t, 268, got.InputTokens)
		assert.Equal(t, 268, got.InputTokensDetails.TextTokens, "all input counted as text when no modality details")
		assert.Equal(t, 0, got.InputTokensDetails.ImageTokens)
		assert.Equal(t, 1024, got.OutputTokens)
		assert.Equal(t, 268+1024, got.TotalTokens)
	})

	t.Run("prompt modality details split into text and image tokens", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     300,
			CandidatesTokenCount: 512,
			PromptTokensDetails: []*genai.ModalityTokenCount{
				{Modality: genai.MediaModality(genai.MediaModalityText), TokenCount: 100},
				{Modality: genai.MediaModality(genai.MediaModalityImage), TokenCount: 200},
			},
		}
		got := convertVertexUsageToImageUsage(meta)
		require.NotNil(t, got)
		assert.Equal(t, 300, got.InputTokens)
		assert.Equal(t, 100, got.InputTokensDetails.TextTokens)
		assert.Equal(t, 200, got.InputTokensDetails.ImageTokens)
		assert.Equal(t, 512, got.OutputTokens)
		assert.Equal(t, 812, got.TotalTokens)
	})

	t.Run("video prompt tokens counted as image tokens", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     150,
			CandidatesTokenCount: 256,
			PromptTokensDetails: []*genai.ModalityTokenCount{
				{Modality: genai.MediaModality(genai.MediaModalityText), TokenCount: 50},
				{Modality: genai.MediaModality(genai.MediaModalityVideo), TokenCount: 100},
			},
		}
		got := convertVertexUsageToImageUsage(meta)
		require.NotNil(t, got)
		assert.Equal(t, 50, got.InputTokensDetails.TextTokens)
		assert.Equal(t, 100, got.InputTokensDetails.ImageTokens)
	})

	t.Run("nil modality detail entries are skipped", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     100,
			CandidatesTokenCount: 200,
			PromptTokensDetails: []*genai.ModalityTokenCount{
				nil,
				{Modality: genai.MediaModality(genai.MediaModalityText), TokenCount: 100},
			},
		}
		got := convertVertexUsageToImageUsage(meta)
		require.NotNil(t, got)
		assert.Equal(t, 100, got.InputTokensDetails.TextTokens)
		assert.Equal(t, 0, got.InputTokensDetails.ImageTokens)
	})

	t.Run("zero token counts produce zero usage", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{}
		got := convertVertexUsageToImageUsage(meta)
		require.NotNil(t, got)
		assert.Equal(t, 0, got.InputTokens)
		assert.Equal(t, 0, got.InputTokensDetails.TextTokens)
		assert.Equal(t, 0, got.InputTokensDetails.ImageTokens)
		assert.Equal(t, 0, got.OutputTokens)
		assert.Equal(t, 0, got.TotalTokens)
	})

	t.Run("total equals input plus output", func(t *testing.T) {
		meta := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     400,
			CandidatesTokenCount: 600,
		}
		got := convertVertexUsageToImageUsage(meta)
		assert.Equal(t, got.InputTokens+got.OutputTokens, got.TotalTokens)
	})
}

func TestVertexChatResponseToOpenAIImage_UsageFormat(t *testing.T) {
	t.Run("response without usage metadata omits usage field", func(t *testing.T) {
		body := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw=="}}]}}]}`
		result, err := VertexChatResponseToOpenAIImage([]byte(body))
		require.NoError(t, err)

		var resp openai.OpenAIImageResponse
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Nil(t, resp.Usage)
	})

	t.Run("response with usage metadata returns image usage format", func(t *testing.T) {
		body := `{
			"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw=="}}]}}],
			"usageMetadata":{"promptTokenCount":268,"candidatesTokenCount":1024,"totalTokenCount":1292}
		}`
		result, err := VertexChatResponseToOpenAIImage([]byte(body))
		require.NoError(t, err)

		var resp openai.OpenAIImageResponse
		require.NoError(t, json.Unmarshal(result, &resp))
		require.NotNil(t, resp.Usage)
		assert.Equal(t, 268, resp.Usage.InputTokens)
		assert.Equal(t, 1024, resp.Usage.OutputTokens)
		assert.Equal(t, 1292, resp.Usage.TotalTokens)
	})

	t.Run("usage JSON has input_tokens not prompt_tokens", func(t *testing.T) {
		body := `{
			"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw=="}}]}}],
			"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":200}
		}`
		result, err := VertexChatResponseToOpenAIImage([]byte(body))
		require.NoError(t, err)

		// Validate raw JSON field names — LiteLLM validates these directly
		var raw map[string]interface{}
		require.NoError(t, json.Unmarshal(result, &raw))
		usageRaw, ok := raw["usage"].(map[string]interface{})
		require.True(t, ok, "usage field must be present")
		assert.Contains(t, usageRaw, "input_tokens", "must have input_tokens (not prompt_tokens)")
		assert.Contains(t, usageRaw, "input_tokens_details", "must have input_tokens_details")
		assert.Contains(t, usageRaw, "output_tokens", "must have output_tokens (not completion_tokens)")
		assert.NotContains(t, usageRaw, "prompt_tokens", "must not have prompt_tokens (chat format)")
		assert.NotContains(t, usageRaw, "completion_tokens", "must not have completion_tokens (chat format)")
	})

	t.Run("no images in candidates returns empty data", func(t *testing.T) {
		body := `{"candidates":[{"content":{"parts":[{"text":"no image here"}]}}]}`
		result, err := VertexChatResponseToOpenAIImage([]byte(body))
		require.NoError(t, err)

		var resp openai.OpenAIImageResponse
		require.NoError(t, json.Unmarshal(result, &resp))
		assert.Empty(t, resp.Data)
	})
}

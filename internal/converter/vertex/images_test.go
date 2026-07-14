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

func TestMapGeminiImageSize(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		size        string
		aspectRatio string
		imageSize   string
	}{
		{name: "2.5 fixed 1K", model: "gemini-2.5-flash-image", size: "1792x2400", aspectRatio: "3:4"},
		{name: "2.5 native dimensions", model: "gemini-2.5-flash-image", size: "896x1152", aspectRatio: "4:5"},
		{name: "3 Pro 1K", model: "gemini-3-pro-image-preview", size: "896x1152", aspectRatio: "4:5", imageSize: "1K"},
		{name: "3 Pro 2K", model: "gemini-3-pro-image", size: "1792x2400", aspectRatio: "3:4", imageSize: "2K"},
		{name: "3 Pro 4K", model: "google/gemini-3-pro-image", size: "3584x4800", aspectRatio: "3:4", imageSize: "4K"},
		{name: "3.1 Flash 512", model: "gemini-3.1-flash-image", size: "512x512", aspectRatio: "1:1", imageSize: "512"},
		{name: "3.1 Flash 1K", model: "gemini-3.1-flash-image-preview", size: "768x1024", aspectRatio: "3:4", imageSize: "1K"},
		{name: "3.1 Flash 2K", model: "gemini-3.1-flash-image", size: "1792x2400", aspectRatio: "3:4", imageSize: "2K"},
		{name: "3.1 Flash nearest 4:5", model: "gemini-3.1-flash-image", size: "896x1152", aspectRatio: "4:5", imageSize: "1K"},
		{name: "3.1 Flash nearest 2:3", model: "gemini-3.1-flash-image", size: "640x1024", aspectRatio: "2:3", imageSize: "1K"},
		{name: "3.1 Flash extended ratio", model: "gemini-3.1-flash-image", size: "1:8", aspectRatio: "1:8", imageSize: "1K"},
		{name: "3.1 Flash ratio with x", model: "gemini-3.1-flash-image", size: "16x9", aspectRatio: "16:9", imageSize: "1K"},
		{name: "3.1 Flash Lite fixed 1K", model: "gemini-3.1-flash-lite-image", size: "1792x2400", aspectRatio: "3:4", imageSize: "1K"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := mapGeminiImageSize(tt.model, tt.size)
			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Equal(t, tt.aspectRatio, config.aspectRatio)
			assert.Equal(t, tt.imageSize, config.imageSize)
		})
	}
}

func TestMapGeminiImageSizeAuto(t *testing.T) {
	config, err := mapGeminiImageSize("gemini-3.1-flash-image", "auto")
	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestMapGeminiImageSizeRejectsInvalidValue(t *testing.T) {
	config, err := mapGeminiImageSize("gemini-3.1-flash-image", "not-a-size")
	assert.Error(t, err)
	assert.Nil(t, config)
	assert.Contains(t, err.Error(), "expected WxH or W:H")
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
		input := `{"model": "gemini-3-pro-image", "prompt": "A landscape", "size": "1792x1024"}`
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

	t.Run("2.5 image request omits unsupported image size", func(t *testing.T) {
		input := `{"model": "gemini-2.5-flash-image", "prompt": "A landscape", "size": "1792x1024"}`
		result, err := ImageRequestToOpenAIChatRequest([]byte(input))
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		require.NoError(t, json.Unmarshal(result, &chatReq))
		genConfig := chatReq.ExtraBody["generation_config"].(map[string]interface{})
		imageConfig := genConfig["image_config"].(map[string]interface{})
		assert.Equal(t, "16:9", imageConfig["aspectRatio"])
		assert.NotContains(t, imageConfig, "imageSize")
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
	buildMultipart := func(t *testing.T, model, size string) ([]byte, string) {
		t.Helper()

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		require.NoError(t, writer.WriteField("model", model))
		require.NoError(t, writer.WriteField("prompt", "Make the object blue"))
		require.NoError(t, writer.WriteField("n", "2"))
		require.NoError(t, writer.WriteField("size", size))

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
		body, contentType := buildMultipart(t, "gemini-2.5-flash-image-preview", "1792x1024")
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
		assert.NotContains(t, imageConfig, "imageSize")
	})

	t.Run("multipart edit uses model size profile", func(t *testing.T) {
		body, contentType := buildMultipart(t, "gemini-3.1-flash-image-preview", "1792x2400")
		result, err := ImageEditRequestToOpenAIChatRequest(body, contentType)
		require.NoError(t, err)

		var chatReq openai.OpenAIRequest
		require.NoError(t, json.Unmarshal(result, &chatReq))
		genConfig := chatReq.ExtraBody["generation_config"].(map[string]interface{})
		imageConfig := genConfig["image_config"].(map[string]interface{})
		assert.Equal(t, "3:4", imageConfig["aspectRatio"])
		assert.Equal(t, "2K", imageConfig["imageSize"])
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

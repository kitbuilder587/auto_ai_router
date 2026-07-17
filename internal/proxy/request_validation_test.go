package proxy

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRequestBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		path        string
		body        string
		wantParam   string
		wantInvalid bool
	}{
		{name: "valid chat", path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{name: "malformed json", path: "/v1/chat/completions", body: `{"model":`, wantParam: "body", wantInvalid: true},
		{name: "top level null", path: "/v1/chat/completions", body: `null`, wantParam: "body", wantInvalid: true},
		{name: "model number", path: "/v1/chat/completions", body: `{"model":7,"messages":[]}`, wantParam: "model", wantInvalid: true},
		{name: "chat messages missing", path: "/v1/chat/completions", body: `{"model":"m"}`, wantParam: "messages", wantInvalid: true},
		{name: "chat messages wrong type", path: "/v1/chat/completions", body: `{"model":"m","messages":"bad"}`, wantParam: "messages", wantInvalid: true},
		{name: "chat messages empty", path: "/v1/chat/completions", body: `{"model":"m","messages":[]}`, wantParam: "messages", wantInvalid: true},
		{name: "stream wrong type", path: "/v1/chat/completions", body: `{"model":"m","messages":[{}],"stream":"yes"}`, wantParam: "stream", wantInvalid: true},
		{name: "valid completion string", path: "/v1/completions", body: `{"model":"m","prompt":"hi"}`},
		{name: "valid completion tokens", path: "/v1/completions", body: `{"model":"m","prompt":[1,2,3]}`},
		{name: "completion prompt object", path: "/v1/completions", body: `{"model":"m","prompt":{}}`, wantParam: "prompt", wantInvalid: true},
		{name: "embedding empty input", path: "/v1/embeddings", body: `{"model":"m","input":[]}`, wantParam: "input", wantInvalid: true},
		{name: "valid embedding batch", path: "/v1/embeddings", body: `{"model":"m","input":["one","two"]}`},
		{name: "responses input missing", path: "/v1/responses", body: `{"model":"m"}`, wantParam: "input", wantInvalid: true},
		{name: "valid structured responses input", path: "/v1/responses", body: `{"model":"m","input":[{"role":"user","content":"hi"}]}`},
		{name: "image prompt missing", path: "/v1/images/generations", body: `{"model":"m"}`, wantParam: "prompt", wantInvalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateRequestBody(tt.path, "application/json", []byte(tt.body))
			if !tt.wantInvalid {
				assert.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			assert.Equal(t, tt.wantParam, err.Param)
		})
	}
}

func TestValidateMultipartImageEdit(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T, includeImage bool) ([]byte, string) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-1"))
		require.NoError(t, writer.WriteField("prompt", "edit"))
		if includeImage {
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", `form-data; name="image"; filename="input.png"`)
			header.Set("Content-Type", "image/png")
			part, err := writer.CreatePart(header)
			require.NoError(t, err)
			_, err = part.Write([]byte("png"))
			require.NoError(t, err)
		}
		require.NoError(t, writer.Close())
		return body.Bytes(), writer.FormDataContentType()
	}

	validBody, validType := build(t, true)
	assert.Nil(t, validateRequestBody("/v1/images/edits", validType, validBody))

	invalidBody, invalidType := build(t, false)
	err := validateRequestBody("/v1/images/edits", invalidType, invalidBody)
	require.NotNil(t, err)
	assert.Equal(t, "image", err.Param)
}

# Google Vertex AI / Gemini API Reference

> **Status:** Partial
>
> **Verified against official sources on:** 2026-03-23
>
> **Confidence:** Medium. Endpoint and schema coverage are broadly aligned with Google documentation, but several advanced capability claims still require tighter verification.
>
> **Official sources:**
>
> - [Vertex AI GenerateContent](https://cloud.google.com/vertex-ai/generative-ai/docs/model-reference/inference)
> - [GenerateContentResponse schema](https://cloud.google.com/vertex-ai/generative-ai/docs/reference/rest/v1/GenerateContentResponse)
> - [Thinking docs](https://cloud.google.com/vertex-ai/generative-ai/docs/thinking)
> - [GenerationConfig reference](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/reference/rest/v1beta1/GenerationConfig)

______________________________________________________________________

## Endpoints

### Vertex AI

```
POST https://{LOCATION}-aiplatform.googleapis.com/v1/projects/{PROJECT_ID}/locations/{LOCATION}/publishers/google/models/{MODEL_ID}:generateContent
POST https://{LOCATION}-aiplatform.googleapis.com/v1/projects/{PROJECT_ID}/locations/{LOCATION}/publishers/google/models/{MODEL_ID}:streamGenerateContent
```

### Gemini AI Studio (Google AI)

```
POST https://generativelanguage.googleapis.com/v1/models/{MODEL_ID}:generateContent?key={API_KEY}
POST https://generativelanguage.googleapis.com/v1/models/{MODEL_ID}:streamGenerateContent?key={API_KEY}
```

______________________________________________________________________

## Request Structure

```json
{
  "contents": [...],
  "systemInstruction": {...},
  "tools": [...],
  "toolConfig": {...},
  "safetySettings": [...],
  "generationConfig": {...},
  "cachedContent": "cachedContents/{id}",
  "labels": {"key": "value"}
}
```

______________________________________________________________________

## Contents & Parts

### Content Object

```json
{
  "role": "user",
  "parts": [...]
}
```

Roles: `"user"`, `"model"`.

### Part Types

#### Text

```json
{"text": "Hello, world!"}
```

#### Inline Data (images, audio, video)

```json
{
  "inlineData": {
    "mimeType": "image/jpeg",
    "data": "<base64>"
  }
}
```

Supports up to 3,000 images per request (Gemini 2.0 Flash).

#### File Data (Cloud Storage, URLs)

```json
{
  "fileData": {
    "mimeType": "video/mp4",
    "fileUri": "gs://bucket/video.mp4"
  }
}
```

Also supports `https://` URLs and YouTube video URLs.

#### Function Call (model output)

```json
{
  "functionCall": {
    "name": "get_weather",
    "args": {"city": "Paris"}
  }
}
```

#### Function Response (user input)

```json
{
  "functionResponse": {
    "name": "get_weather",
    "response": {"temperature": 22}
  }
}
```

#### Executable Code (code execution output)

```json
{
  "executableCode": {
    "language": "PYTHON",
    "code": "print('hello')"
  }
}
```

#### Code Execution Result

```json
{
  "codeExecutionResult": {
    "outcome": "OUTCOME_OK",
    "output": "hello"
  }
}
```

#### Thought (model reasoning)

```json
{
  "thought": true,
  "text": "Let me think about this..."
}
```

Boolean flag indicating this part contains model reasoning.

#### Thought Signature

```json
{
  "thoughtSignature": "<opaque_base64>"
}
```

Opaque signature for replaying thoughts in multi-turn conversations.

#### Video Metadata

```json
{
  "videoMetadata": {
    "startOffset": {"seconds": 0},
    "endOffset": {"seconds": 30}
  }
}
```

______________________________________________________________________

## System Instruction

```json
{
  "systemInstruction": {
    "parts": [
      {"text": "You are a helpful assistant."}
    ]
  }
}
```

Text-only content for system-level directives.

______________________________________________________________________

## GenerationConfig

| Parameter          | Type     | Default      | Description                                                                            |
| ------------------ | -------- | ------------ | -------------------------------------------------------------------------------------- |
| `temperature`      | float    | 1.0          | Sampling randomness (0.0–2.0)                                                          |
| `topP`             | float    | 0.95         | Nucleus sampling threshold (0.0–1.0)                                                   |
| `topK`             | integer  | —            | Limits token selection to top-K candidates                                             |
| `maxOutputTokens`  | integer  | —            | Maximum response length in tokens                                                      |
| `candidateCount`   | integer  | 1            | Number of response variations (1–8 for Gemini 2.0+)                                    |
| `stopSequences`    | string[] | —            | Up to 5 case-sensitive stop strings                                                    |
| `presencePenalty`  | float    | 0            | Penalize already-present tokens (-2.0 to 2.0)                                          |
| `frequencyPenalty` | float    | 0            | Penalize repeated tokens (-2.0 to 2.0)                                                 |
| `responseMimeType` | string   | `text/plain` | Output format: `text/plain`, `application/json`, `text/x.enum`                         |
| `responseSchema`   | Schema   | —            | JSON Schema for structured output (requires non-plain MIME type)                       |
| `seed`             | integer  | —            | Reproducible output (best-effort)                                                      |
| `responseLogprobs` | boolean  | false        | Enable token probability logging                                                       |
| `logprobs`         | integer  | —            | Return top candidate tokens (1–20)                                                     |
| `audioTimestamp`   | boolean  | false        | Enable timestamp understanding for audio-only files                                    |
| `thinkingConfig`   | object   | —            | Control reasoning process (Gemini 2.5+)                                                |
| `mediaResolution`  | enum     | —            | Reduce token usage per media item: `HIGH`, `MEDIUM`, `LOW`                             |
| `speechConfig`     | object   | —            | Voice output config: `{"voiceConfig": {"prebuiltVoiceConfig": {"voiceName": "Kore"}}}` |

______________________________________________________________________

## ThinkingConfig

### Gemini 2.5 Models (Token Budget)

```json
{
  "thinkingConfig": {
    "thinkingBudget": 8192,
    "includeThoughts": true
  }
}
```

| Field             | Type    | Description                                                 |
| ----------------- | ------- | ----------------------------------------------------------- |
| `thinkingBudget`  | integer | Token budget for reasoning. `0` = disabled, `-1` = dynamic. |
| `includeThoughts` | boolean | Include thought text in response parts.                     |

Special cases:

- `gemini-2.5-pro`: Cannot disable thinking (`budget=0` is invalid). Use `-1` for dynamic.
- `gemini-2.5-flash`: `budget=0` disables thinking.

### Gemini 3+ Models (Thinking Level)

```json
{
  "thinkingConfig": {
    "thinkingLevel": "HIGH",
    "includeThoughts": true
  }
}
```

| Field             | Type    | Description                             |
| ----------------- | ------- | --------------------------------------- |
| `thinkingLevel`   | string  | Reasoning depth enum.                   |
| `includeThoughts` | boolean | Include thought text in response parts. |

**ThinkingLevel values by model variant:**

| Level     | Flash / Flash-Lite  | Pro                        |
| --------- | ------------------- | -------------------------- |
| `MINIMAL` | Supported           | Not supported (use `LOW`)  |
| `LOW`     | Supported           | Supported                  |
| `MEDIUM`  | Supported           | Not supported (use `HIGH`) |
| `HIGH`    | Supported (default) | Supported (default)        |

> **Important:** Cannot specify both `thinkingLevel` and `thinkingBudget` in the same request for Gemini 3 models (will error). `thinkingBudget` is accepted for backwards compatibility on Gemini 3 but may produce unexpected results on Pro.

______________________________________________________________________

## Tool Types

Each tool should contain exactly one type.

### Function Declarations

```json
{
  "functionDeclarations": [
    {
      "name": "get_weather",
      "description": "Get current weather",
      "parameters": {
        "type": "OBJECT",
        "properties": {
          "city": {"type": "STRING", "description": "City name"}
        },
        "required": ["city"]
      }
    }
  ]
}
```

### Google Search

```json
{
  "googleSearch": {}
}
```

For Gemini 2.0+ models. Enables grounded web search.

### Google Search Retrieval (Legacy)

```json
{
  "googleSearchRetrieval": {
    "dynamicRetrievalConfig": {
      "mode": "MODE_DYNAMIC",
      "dynamicThreshold": 0.7
    }
  }
}
```

### Code Execution

```json
{
  "codeExecution": {}
}
```

Model can write and execute Python code.

### URL Context

```json
{
  "urlContext": {}
}
```

Fetches live web page content for URLs in the prompt. Two-stage process: Google index first, then direct fetch if needed.

### Google Maps

```json
{
  "googleMaps": {}
}
```

Enables location-aware responses with Google Maps data.

______________________________________________________________________

## ToolConfig

```json
{
  "toolConfig": {
    "functionCallingConfig": {
      "mode": "AUTO",
      "allowedFunctionNames": ["get_weather"]
    }
  }
}
```

| Mode   | Description                             |
| ------ | --------------------------------------- |
| `AUTO` | Model decides whether to call functions |
| `ANY`  | Must call at least one function         |
| `NONE` | Never call functions                    |

`allowedFunctionNames`: When `mode` is `ANY`, restrict to these specific functions.

______________________________________________________________________

## Response Schema

```json
{
  "candidates": [...],
  "usageMetadata": {...},
  "modelVersion": "gemini-2.5-flash-001",
  "createTime": "2026-03-20T...",
  "responseId": "abc123",
  "promptFeedback": {...}
}
```

### Candidate Object

| Field                | Type    | Description                                |
| -------------------- | ------- | ------------------------------------------ |
| `index`              | integer | Position in response list                  |
| `content`            | Content | Generated content (parts array)            |
| `finishReason`       | string  | Why generation stopped                     |
| `safetyRatings`      | array   | Per-category harm ratings                  |
| `citationMetadata`   | object  | Source attributions with URI, title, date  |
| `groundingMetadata`  | object  | Grounding sources                          |
| `urlContextMetadata` | object  | Retrieved URL information                  |
| `avgLogprobs`        | float   | Average log probability (confidence score) |
| `logprobsResult`     | object  | Detailed token probability data            |
| `finishMessage`      | string  | Human-readable stop reason                 |

______________________________________________________________________

## FinishReason Values

| Value                       | Description                                    |
| --------------------------- | ---------------------------------------------- |
| `STOP`                      | Natural stop or hit stop sequence              |
| `MAX_TOKENS`                | Hit `maxOutputTokens` limit                    |
| `SAFETY`                    | Blocked by safety filter                       |
| `RECITATION`                | Blocked due to recitation/copyright            |
| `BLOCKLIST`                 | Blocked by term blocklist                      |
| `PROHIBITED_CONTENT`        | Blocked prohibited content                     |
| `SPII`                      | Blocked sensitive personally identifiable info |
| `MALFORMED_FUNCTION_CALL`   | Invalid function call generated                |
| `OTHER`                     | Other/unspecified reason                       |
| `FINISH_REASON_UNSPECIFIED` | Not set                                        |
| `MODEL_ARMOR`               | Blocked by Model Armor                         |
| `IMAGE_SAFETY`              | Image blocked by safety                        |
| `IMAGE_PROHIBITED_CONTENT`  | Image contains prohibited content              |
| `IMAGE_RECITATION`          | Image blocked for recitation                   |
| `IMAGE_OTHER`               | Image blocked for other reason                 |
| `UNEXPECTED_TOOL_CALL`      | Unexpected tool call                           |
| `NO_IMAGE`                  | No image generated                             |
| `TOOL_CALL`                 | Model wants to invoke a tool                   |

______________________________________________________________________

## UsageMetadata

```json
{
  "promptTokenCount": 100,
  "candidatesTokenCount": 50,
  "totalTokenCount": 170,
  "cachedContentTokenCount": 30,
  "thoughtsTokenCount": 20,
  "toolUsePromptTokenCount": 10,
  "promptTokensDetails": [
    {"modality": "TEXT", "tokenCount": 80},
    {"modality": "IMAGE", "tokenCount": 20}
  ],
  "candidatesTokensDetails": [
    {"modality": "TEXT", "tokenCount": 50}
  ],
  "cacheTokensDetails": [
    {"modality": "TEXT", "tokenCount": 30}
  ],
  "toolUsePromptTokensDetails": [
    {"modality": "TEXT", "tokenCount": 10}
  ],
  "trafficType": "ON_DEMAND"
}
```

### Token Count Fields

| Field                        | Type    | Description                                                                 |
| ---------------------------- | ------- | --------------------------------------------------------------------------- |
| `promptTokenCount`           | integer | Total input tokens                                                          |
| `candidatesTokenCount`       | integer | Output tokens (Vertex AI: excludes thoughts; Gemini API: includes thoughts) |
| `totalTokenCount`            | integer | Sum of prompt + candidates + thoughts + toolUse                             |
| `cachedContentTokenCount`    | integer | Tokens served from cache                                                    |
| `thoughtsTokenCount`         | integer | Tokens used for model reasoning                                             |
| `toolUsePromptTokenCount`    | integer | Tokens from tool execution results fed back to model                        |
| `promptTokensDetails`        | array   | Per-modality breakdown of input tokens                                      |
| `candidatesTokensDetails`    | array   | Per-modality breakdown of output tokens                                     |
| `cacheTokensDetails`         | array   | Per-modality breakdown of cached tokens                                     |
| `toolUsePromptTokensDetails` | array   | Per-modality breakdown of tool use tokens                                   |
| `trafficType`                | string  | `ON_DEMAND`, `PROVISIONED_THROUGHPUT`, etc.                                 |

### Modality Values

`TEXT`, `IMAGE`, `AUDIO`, `VIDEO`, `DOCUMENT`.

> **Important difference:** On Vertex AI, `candidatesTokenCount` does NOT include thinking tokens (they are separate in `thoughtsTokenCount`). On the Gemini API (AI Studio), `candidatesTokenCount` INCLUDES thinking tokens.

______________________________________________________________________

## LogprobsResult

```json
{
  "topCandidates": [
    {
      "candidates": [
        {"token": "Hello", "tokenId": 12345, "logProbability": -0.1},
        {"token": "Hi", "tokenId": 67890, "logProbability": -2.3}
      ]
    }
  ],
  "chosenCandidates": [
    {"token": "Hello", "tokenId": 12345, "logProbability": -0.1}
  ]
}
```

Two parallel arrays:

- `topCandidates[]` — alternative tokens considered at each position
- `chosenCandidates[]` — actual tokens selected

______________________________________________________________________

## Safety Settings

```json
{
  "safetySettings": [
    {
      "category": "HARM_CATEGORY_HATE_SPEECH",
      "threshold": "BLOCK_MEDIUM_AND_ABOVE",
      "method": "PROBABILITY"
    }
  ]
}
```

### Categories

| Category                          |
| --------------------------------- |
| `HARM_CATEGORY_HATE_SPEECH`       |
| `HARM_CATEGORY_DANGEROUS_CONTENT` |
| `HARM_CATEGORY_HARASSMENT`        |
| `HARM_CATEGORY_SEXUALLY_EXPLICIT` |

### Thresholds

| Threshold                | Description                     |
| ------------------------ | ------------------------------- |
| `BLOCK_NONE` / `OFF`     | No blocking                     |
| `BLOCK_ONLY_HIGH`        | Block only high probability     |
| `BLOCK_MEDIUM_AND_ABOVE` | Block medium and above          |
| `BLOCK_LOW_AND_ABOVE`    | Block low and above (strictest) |

### Methods

| Method        | Description                 |
| ------------- | --------------------------- |
| `PROBABILITY` | Probability-based (default) |
| `SEVERITY`    | Severity-based              |

______________________________________________________________________

## Streaming

The `streamGenerateContent` endpoint returns chunks as they are generated. Each chunk has the same `GenerateContentResponse` structure but with partial content.

Token usage (`usageMetadata`) is typically included in the final chunk only. Thought parts (`thought: true`) appear in streaming chunks as they are generated, followed by `thoughtSignature` parts.

______________________________________________________________________

## Supported MIME Types

### Images

`image/jpeg`, `image/png`, `image/gif`, `image/webp`

### Video

`video/mp4`, `video/mpeg`, `video/mov`, `video/avi`, `video/x-flv`, `video/mpg`, `video/webm`, `video/wmv`, `video/3gpp`

### Audio

`audio/wav`, `audio/mp3`, `audio/mpeg`, `audio/aiff`, `audio/aac`, `audio/ogg`, `audio/flac`

### Documents

`application/pdf`, `text/plain`

"""
Vertex AI thinking / reasoning parameter tests.

Covers scenarios from debug logs:
  - gemini-2.5-pro: "thinking_budget to 0" — model rejects budget=0
  - gemini-3-pro-preview: "thinking_level MINIMAL" — model rejects MINIMAL
  - nullable: true in JSON schema causing hallucinations vs anyOf [{type:null}]

Run:
    pytest tests/vertex/test_thinking.py -v -s
    pytest tests/vertex/test_thinking.py -v -s -k "pro"
"""

import pytest
import re
from test_helpers import TestModels, ResponseValidator, ContentValidator, StreamingValidator

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _simple_messages():
    return [{"role": "user", "content": "Ответь одним словом: работаешь?"}]


_ALLOWED_SHORT_ANSWERS = {
    "да",
    "нет",
    "работаю",
    "работает",
    "работаем",
    "yes",
    "no",
    "working",
    "works",
    "work",
    "ok",
    "okay",
    "ага",
    "конечно",
}


def _assert_non_gibberish_answer(content: str) -> None:
    assert isinstance(content, str), f"Expected string content, got: {type(content)!r}"
    text = content.strip()
    assert len(text) > 0, "Response should not be empty"
    assert len(text) <= 200, f"Response is too long for this short-answer test: {len(text)} characters"

    words = re.findall(r"[A-Za-zА-Яа-яЁё]+", text)
    assert words, f"Response must contain at least one word: {text!r}"
    assert any(len(word) > 1 for word in words), (
        f"Response should use a real word, got: {words}")

    vowels = set("aeiouyаеёиоуыэюя")
    assert any(ch in vowels for ch in text.lower()), (
        f"Response looks like gibberish (no vowels): {text!r}")


def _assert_working(response):
    """Model responded — no error raised, content is present."""
    ResponseValidator.validate_chat_response(response)
    content = response.choices[0].message.content
    assert content and len(content) > 0
    _assert_non_gibberish_answer(content)


# ---------------------------------------------------------------------------
# Default thinking (no explicit params) — main regression for the bug
# ---------------------------------------------------------------------------

class TestThinkingDefault:
    """Bare requests without any thinking params.

    Regression: gemini-2.5-pro returned 'thinking_budget to 0',
    gemini-3-pro-preview returned 'thinking_level MINIMAL'.
    Both should succeed after the fix (default ThinkingConfig is set correctly).
    """

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    def test_bare_request(self, openai_client, model):
        """Bare request — no thinking params at all."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=64,
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    def test_bare_request_with_temperature(self, openai_client, model):
        """Bare request with temperature only (Gemini 3-pro requires temperature=1.0)."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=64,
            temperature=1.0,
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# reasoning_effort — maps to ThinkingBudget (2.5) / ThinkingLevel (3+)
# ---------------------------------------------------------------------------

class TestReasoningEffort:
    """reasoning_effort parameter for all thinking models."""

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    @pytest.mark.parametrize("effort", ["low", "medium", "high"])
    def test_reasoning_effort(self, openai_client, model, effort):
        """reasoning_effort=low/medium/high should work on all thinking models."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=128,
            extra_body={"reasoning_effort": effort},
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_25)
    def test_reasoning_effort_disable_25(self, openai_client, model):
        """reasoning_effort=disable: flash gets budget=0, pro gets dynamic(-1)."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=64,
            extra_body={"reasoning_effort": "disable"},
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_3)
    def test_reasoning_effort_disable_3(self, openai_client, model):
        """reasoning_effort=disable: flash gets MINIMAL, pro gets LOW."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=64,
            extra_body={"reasoning_effort": "disable"},
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# thinking (Anthropic-style: {"type": "enabled", "budget_tokens": N})
# ---------------------------------------------------------------------------

class TestAnthropicStyleThinking:
    """top-level 'thinking' field — Anthropic SDK style.

    Bug fixed: code was reading from extra_body["thinking"] but not from
    the top-level req.Thinking field, so these params were silently ignored.
    """

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_25)
    def test_thinking_enabled_budget_25(self, openai_client, model):
        """Anthropic-style thinking with budget_tokens for Gemini 2.5."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={
                "thinking": {"type": "enabled", "budget_tokens": 1024}
            },
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_3)
    def test_thinking_enabled_budget_3(self, openai_client, model):
        """Anthropic-style thinking with budget_tokens for Gemini 3 (maps to ThinkingLevel)."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={
                "thinking": {"type": "enabled", "budget_tokens": 8192}
            },
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    def test_thinking_disabled(self, openai_client, model):
        """Anthropic-style thinking disabled — should fall back to minimal thinking."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=64,
            extra_body={
                "thinking": {"type": "disabled", "budget_tokens": 0}
            },
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# thinking_budget (Gemini-style, top-level numeric param)
# ---------------------------------------------------------------------------

class TestThinkingBudget:
    """top-level 'thinking_budget' field (Gemini-native, numeric).

    Bug fixed: OpenAIRequest had no ThinkingBudget field — param was lost on unmarshal.
    """

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_25)
    @pytest.mark.parametrize("budget", [128, 1024, 8192])
    def test_thinking_budget_numeric_25(self, openai_client, model, budget):
        """thinking_budget=<int> for Gemini 2.5 models."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={"thinking_budget": budget},
        )
        _assert_working(response)

    def test_thinking_budget_zero_flash(self, openai_client):
        """thinking_budget=0 disables thinking on gemini-2.5-flash (valid)."""
        response = openai_client.chat.completions.create(
            model="gemini-2.5-flash",
            messages=_simple_messages(),
            max_tokens=64,
            extra_body={"thinking_budget": 0},
        )
        _assert_working(response)

    def test_thinking_budget_zero_pro_uses_dynamic(self, openai_client):
        """thinking_budget=0 for gemini-2.5-pro must NOT error (converted to dynamic -1)."""
        response = openai_client.chat.completions.create(
            model="gemini-2.5-pro",
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={"thinking_budget": 0},
        )
        _assert_working(response)

    def test_thinking_budget_dynamic_pro(self, openai_client):
        """thinking_budget=-1 (dynamic) for gemini-2.5-pro."""
        response = openai_client.chat.completions.create(
            model="gemini-2.5-pro",
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={"thinking_budget": -1},
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# thinking_level (Gemini 3-style, top-level string param)
# ---------------------------------------------------------------------------

class TestThinkingLevel:
    """top-level 'thinking_level' field (Gemini 3-native).

    Bug fixed: OpenAIRequest had no ThinkingLevel field — param was lost on unmarshal.
    """

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_3)
    @pytest.mark.parametrize("level", ["low", "medium", "high"])
    def test_thinking_level_standard(self, openai_client, model, level):
        """thinking_level=low/medium/high for Gemini 3 models."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={"thinking_level": level},
        )
        _assert_working(response)

    def test_thinking_level_minimal_flash(self, openai_client):
        """thinking_level=minimal is valid for gemini-3-flash-preview."""
        response = openai_client.chat.completions.create(
            model="gemini-3-flash-preview",
            messages=_simple_messages(),
            max_tokens=64,
            extra_body={"thinking_level": "minimal"},
        )
        _assert_working(response)

    def test_thinking_level_minimal_pro_maps_to_low(self, openai_client):
        """thinking_level=minimal for gemini-3-pro-preview must NOT error
        (converted to LOW since MINIMAL is unsupported on pro variants)."""
        response = openai_client.chat.completions.create(
            model="gemini-3-pro-preview",
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={"thinking_level": "minimal"},
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# extra_body.thinking_config (Gemini-native nested config)
# ---------------------------------------------------------------------------

class TestNativeThinkingConfig:
    """extra_body.thinking_config — highest priority source."""

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_25)
    def test_native_config_budget_25(self, openai_client, model):
        """extra_body.thinking_config.thinking_budget for Gemini 2.5."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={
                "thinking_config": {"thinking_budget": 2048, "include_thoughts": True}
            },
        )
        _assert_working(response)

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING_3)
    def test_native_config_level_3(self, openai_client, model):
        """extra_body.thinking_config.thinking_level for Gemini 3."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={
                "thinking_config": {"thinking_level": "high", "include_thoughts": True}
            },
        )
        _assert_working(response)

    def test_native_config_takes_priority_over_reasoning_effort(self, openai_client):
        """thinking_config has higher priority than reasoning_effort."""
        response = openai_client.chat.completions.create(
            model="gemini-2.5-flash",
            messages=_simple_messages(),
            max_tokens=256,
            extra_body={
                "reasoning_effort": "high",
                "thinking_config": {"thinking_budget": 512},
            },
        )
        _assert_working(response)


# ---------------------------------------------------------------------------
# Streaming with thinking params
# ---------------------------------------------------------------------------

class TestThinkingStreaming:
    """Thinking params with streaming enabled."""

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    def test_streaming_bare(self, openai_client, model):
        """Streaming bare request — no thinking params."""
        stream = openai_client.chat.completions.create(
            model=model,
            messages=_simple_messages(),
            max_tokens=512,
            stream=True,
        )
        full_content, chunk_count = StreamingValidator.collect_streaming_content(stream)
        StreamingValidator.assert_valid_streaming_response(full_content, chunk_count)
        _assert_non_gibberish_answer(full_content)

    def test_streaming_gemini25pro_with_budget(self, openai_client):
        """Streaming gemini-2.5-pro with explicit thinking_budget."""
        stream = openai_client.chat.completions.create(
            model="gemini-2.5-pro",
            messages=_simple_messages(),
            max_tokens=256,
            stream=True,
            extra_body={"thinking_budget": 1024},
        )
        full_content, chunk_count = StreamingValidator.collect_streaming_content(stream)
        StreamingValidator.assert_valid_streaming_response(full_content, chunk_count)
        _assert_non_gibberish_answer(full_content)

    def test_streaming_gemini3pro_with_level_high(self, openai_client):
        """Streaming gemini-3-pro-preview with thinking_level=high."""
        stream = openai_client.chat.completions.create(
            model="gemini-3-pro-preview",
            messages=_simple_messages(),
            max_tokens=256,
            stream=True,
            extra_body={"thinking_level": "high"},
        )
        full_content, chunk_count = StreamingValidator.collect_streaming_content(stream)
        StreamingValidator.assert_valid_streaming_response(full_content, chunk_count)
        _assert_non_gibberish_answer(full_content)


# ---------------------------------------------------------------------------
# Nullable JSON schema debug fixture
# ---------------------------------------------------------------------------

_PERSON_ANYOF_SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "person_info",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {
                "full_name": {"type": "string"},
                "position": {
                    "anyOf": [{"type": "string"}, {"type": "null"}],
                    "description": "Должность или null",
                },
                "phone": {
                    "anyOf": [{"type": "string"}, {"type": "null"}],
                    "description": "Телефон или null",
                },
                "email": {
                    "anyOf": [{"type": "string"}, {"type": "null"}],
                    "description": "Email или null",
                },
            },
            "required": ["full_name", "position", "phone", "email"],
        },
    },
}

_PERSON_NULLABLE_SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "person_info",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {
                "full_name": {"type": "string"},
                "position": {
                    "type": "string",
                    "description": "Должность или null",
                    "nullable": True,
                },
                "phone": {
                    "type": "string",
                    "description": "Телефон или null",
                    "nullable": True,
                },
                "email": {
                    "type": "string",
                    "description": "Email или null",
                    "nullable": True,
                },
            },
            "required": ["full_name", "position", "phone", "email"],
        },
    },
}

_PERSON_PROMPT = (
    "Извлеки данные о человеке из текста. Если данных нет — верни null.\n\n"
    "Текст: \"Иванов Иван работает программистом в Рога и Копыта. "
    "Телефон и email неизвестны.\"\n\nВерни JSON строго по схеме."
)

_ENUM_ANYOF_SCHEMA = {
    "type": "json_schema",
    "json_schema": {
        "name": "document_extract",
        "strict": True,
        "schema": {
            "type": "object",
            "properties": {
                "source_type": {"type": "string"},
                "document_id": {"type": "string"},
                "government_conclusion": {
                    "anyOf": [
                        {
                            "type": "string",
                            "enum": ["положительное", "отрицательное", "положительное с учетом доработки"],
                        },
                        {"type": "null"},
                    ],
                    "description": "Заключение Правительства: только для СОЗД. Для ЕЭК — null.",
                },
                "amendments_total": {
                    "anyOf": [{"type": "integer"}, {"type": "null"}],
                    "description": "Поправки: только для СОЗД. Для ЕЭК — null.",
                },
            },
            "required": ["source_type", "document_id", "government_conclusion", "amendments_total"],
        },
    },
}

_ENUM_PROMPT = (
    "Ты — структурированный экстрактор данных из документов НПА.\n\n"
    "Источник: ЕЭК (Евразийская экономическая комиссия).\n"
    "Документ ID: eec-doc-2024-001\n"
    "Текст: \"Проект решения Совета ЕЭК о внесении изменений в технический регламент.\"\n\n"
    "ВАЖНО: government_conclusion применяется ТОЛЬКО для СОЗД. Для ЕЭК — null.\n"
    "amendments_total применяется ТОЛЬКО для СОЗД. Для ЕЭК — null.\n\n"
    "Верни JSON строго по схеме."
)


class TestNullableJsonSchema:
    """Nullable fields in JSON schema — critical correctness tests.

    From nullable JSON schema debug fixture:
    - anyOf [{type:string}, {type:null}] → model returns proper JSON null
    - nullable:true (deprecated) → model may return "null" string instead of null
    """

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS)
    def test_nullable_anyof_returns_null(self, openai_client, model):
        """anyOf schema: phone/email must be JSON null (not string 'null')."""
        import json

        response = openai_client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": _PERSON_PROMPT}],
            max_tokens=512,
            temperature=0,
            response_format=_PERSON_ANYOF_SCHEMA,
        )
        ResponseValidator.validate_chat_response(response)
        content = response.choices[0].message.content
        data = json.loads(content) if isinstance(content, str) else content
        assert data["phone"] is None, f"phone must be null, got: {data['phone']!r}"
        assert data["email"] is None, f"email must be null, got: {data['email']!r}"

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS)
    def test_nullable_true_behavior(self, openai_client, model):
        """nullable:true schema: model responds without error (behavior may vary)."""
        response = openai_client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": _PERSON_PROMPT}],
            max_tokens=512,
            temperature=0,
            response_format=_PERSON_NULLABLE_SCHEMA,
        )
        ResponseValidator.validate_chat_response(response)
        # nullable:true is deprecated; content should be parseable JSON
        import json
        content = response.choices[0].message.content
        data = json.loads(content) if isinstance(content, str) else content
        assert "full_name" in data

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS)
    def test_enum_anyof_null_no_hallucination(self, openai_client, model):
        """enum+anyOf null: for ЕЭК source, government_conclusion must be null (not hallucinated enum value)."""
        import json

        response = openai_client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": _ENUM_PROMPT}],
            max_tokens=512,
            temperature=0,
            response_format=_ENUM_ANYOF_SCHEMA,
        )
        ResponseValidator.validate_chat_response(response)
        content = response.choices[0].message.content
        data = json.loads(content) if isinstance(content, str) else content
        assert data["government_conclusion"] is None, (
            f"government_conclusion must be null for ЕЭК source, got: {data['government_conclusion']!r}"
        )
        assert data["amendments_total"] is None, (
            f"amendments_total must be null for ЕЭК source, got: {data['amendments_total']!r}"
        )

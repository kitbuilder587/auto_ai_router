"""
Vertex AI Responses API tests — web_search and built-in tool passthrough.

Regression tests for the fix in internal/converter/responses/request.go:
convertTools() was rejecting all non-function tools (web_search, web_search_preview,
computer_use, etc.) with HTTP 400 "unsupported tool type for chat completions".

After the fix, non-function tools are passed through as-is to provider-specific
converters, which map them correctly (e.g. web_search → GoogleSearch for Vertex).

Known Gemini API limitations:
- Built-in tools (GoogleSearch) and Function Calling CANNOT be combined in one request.
- FunctionCallingConfig requires function_declarations to be present.

Run:
    pytest tests/vertex/test_responses.py -v -s
    pytest tests/vertex/test_responses.py -v -s -k "web_search"
"""

import pytest
from openai.types.responses import (
    Response,
    ResponseCompletedEvent,
    ResponseTextDeltaEvent,
)
from test_helpers import TestModels, ContentValidator


# Vertex models to test Responses API with web_search
VERTEX_RESPONSES_MODELS = TestModels.VERTEX_MODELS

# Models that support google search (all Vertex models)
VERTEX_SEARCH_MODELS = TestModels.VERTEX_MODELS


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def extract_response_text(response: Response) -> str:
    """Extract text content from a Responses API response."""
    texts = []
    for item in response.output:
        if item.type == "message":
            for part in item.content:
                if hasattr(part, "text"):
                    texts.append(part.text)
    return "".join(texts)


def validate_responses_api_response(response: Response) -> None:
    """Validate basic structure of a Responses API response."""
    assert response.id is not None, "response must have an id"
    assert response.model is not None, "response must have a model"
    assert response.output is not None, "response must have output"
    assert len(response.output) > 0, "output must not be empty"

    text = extract_response_text(response)
    assert len(text) > 0, "response must contain text content"


def validate_responses_api_usage(response: Response) -> None:
    """Validate that usage is present and has correct Responses API fields."""
    assert response.usage is not None, "usage must be present"
    assert response.usage.input_tokens > 0, (
        f"input_tokens must be > 0, got {response.usage.input_tokens}"
    )
    assert response.usage.output_tokens > 0, (
        f"output_tokens must be > 0, got {response.usage.output_tokens}"
    )
    assert response.usage.total_tokens > 0, (
        f"total_tokens must be > 0, got {response.usage.total_tokens}"
    )


# ---------------------------------------------------------------------------
# Basic Responses API over Vertex (no special tools)
# ---------------------------------------------------------------------------

class TestVertexResponsesBasic:
    """Basic Responses API calls routed through Vertex AI provider."""

    @pytest.mark.parametrize("model", VERTEX_RESPONSES_MODELS)
    def test_basic_response(self, openai_client, model):
        """Simple Responses API call to Vertex model returns valid response."""
        response = openai_client.responses.create(
            model=model,
            input="What is the capital of France? Answer in one word.",
            max_output_tokens=50,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

        text = extract_response_text(response)
        ContentValidator.assert_contains_any(text, ["Paris", "paris", "Париж"])

    @pytest.mark.parametrize("model", VERTEX_RESPONSES_MODELS)
    def test_message_input_format(self, openai_client, model):
        """Responses API with structured message input via Vertex."""
        response = openai_client.responses.create(
            model=model,
            input=[{"role": "user", "content": "Say hello"}],
            max_output_tokens=50,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

    @pytest.mark.parametrize("model", VERTEX_RESPONSES_MODELS)
    def test_system_instructions(self, openai_client, model):
        """Responses API with system instructions via Vertex."""
        response = openai_client.responses.create(
            model=model,
            instructions="Always respond in exactly one word.",
            input="What color is the sky?",
            max_output_tokens=20,
        )
        validate_responses_api_response(response)


# ---------------------------------------------------------------------------
# web_search / web_search_preview tool — the core regression fix
# ---------------------------------------------------------------------------

class TestVertexResponsesWebSearch:
    """Regression: web_search and web_search_preview tools via Responses API.

    Before the fix, these requests returned HTTP 400:
    "unsupported tool type for chat completions: web_search_preview"

    After the fix:
    1. Non-function tools pass through convertTools() in responses/request.go
    2. Vertex converter maps them to GoogleSearch
    3. FunctionCallingConfig is NOT set when only built-in tools are present
       (Gemini rejects FunctionCallingConfig without function_declarations)
    """

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_web_search_tool(self, openai_client, model):
        """Responses API with tools=[{type: web_search}] must not return 400."""
        response = openai_client.responses.create(
            model=model,
            input="What is the current population of Tokyo? Answer briefly.",
            tools=[{"type": "web_search"}],
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_web_search_preview_tool(self, openai_client, model):
        """Responses API with tools=[{type: web_search_preview}] must not return 400."""
        response = openai_client.responses.create(
            model=model,
            input="What is the latest news about AI? Answer in 1-2 sentences.",
            tools=[{"type": "web_search_preview"}],
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_web_search_without_tool_choice(self, openai_client, model):
        """web_search without explicit tool_choice (model decides freely)."""
        response = openai_client.responses.create(
            model=model,
            input="What is the USD/RUB exchange rate today?",
            tools=[{"type": "web_search_preview"}],
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    @pytest.mark.parametrize("tool_choice", ["auto", "required", "none"])
    def test_web_search_with_tool_choice_string(self, openai_client, model, tool_choice):
        """tool_choice string values with web_search must not crash.

        Users may send any tool_choice value with built-in tools.
        Gemini rejects FunctionCallingConfig without function_declarations,
        so the proxy silently skips ToolConfig for built-in-only tool sets.
        """
        response = openai_client.responses.create(
            model=model,
            input="What year did the Eiffel Tower open? Answer briefly.",
            tools=[{"type": "web_search_preview"}],
            tool_choice=tool_choice,
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_web_search_with_tool_choice_function_object(self, openai_client, model):
        """tool_choice={type: function, name: ...} with web_search must not crash.

        This is a nonsensical combination (no functions declared), but a
        'dumb' user might send it. The proxy should not 500.
        """
        response = openai_client.responses.create(
            model=model,
            input="What is the speed of light?",
            tools=[{"type": "web_search_preview"}],
            tool_choice={"type": "function", "name": "nonexistent"},
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)


# ---------------------------------------------------------------------------
# Gemini API limitation: built-in tools + function calling cannot be combined
# ---------------------------------------------------------------------------

class TestVertexResponsesMixedToolsLimitation:
    """Gemini API does NOT allow combining GoogleSearch with function declarations.

    Error: "Built-in tools ({google_search}) and Function Calling cannot be
    combined in the same request."

    The proxy handles this by preferring built-in tools and dropping functions
    when both are present.
    """

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_mixed_tools_prefers_builtin(self, openai_client, model):
        """When web_search + function are mixed, built-in tool takes priority.

        The Vertex converter drops function declarations when built-in tools
        are present to avoid the Gemini API rejection.
        """
        response = openai_client.responses.create(
            model=model,
            input="What is the weather in Moscow today?",
            tools=[
                {"type": "web_search_preview"},
                {
                    "type": "function",
                    "name": "get_weather",
                    "description": "Get current weather for a city",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "city": {"type": "string", "description": "City name"},
                        },
                        "required": ["city"],
                    },
                },
            ],
            max_output_tokens=200,
        )
        # Should succeed — built-in tool wins, functions are silently dropped
        validate_responses_api_response(response)
        validate_responses_api_usage(response)


# ---------------------------------------------------------------------------
# Streaming with web_search tools
# ---------------------------------------------------------------------------

class TestVertexResponsesWebSearchStreaming:
    """Streaming Responses API with web_search tools via Vertex."""

    @pytest.mark.parametrize("model", VERTEX_SEARCH_MODELS)
    def test_web_search_streaming(self, openai_client, model):
        """Streaming with web_search tool should return text deltas and completed event."""
        collected_text = ""
        got_completed = False
        completed_response = None

        with openai_client.responses.stream(
            model=model,
            input="What is the population of Japan? Answer briefly.",
            tools=[{"type": "web_search_preview"}],
            max_output_tokens=200,
        ) as stream:
            for event in stream:
                if isinstance(event, ResponseTextDeltaEvent):
                    collected_text += event.delta
                elif isinstance(event, ResponseCompletedEvent):
                    got_completed = True
                    completed_response = event.response

        assert len(collected_text) > 0, "should receive text content via streaming"
        assert got_completed, "should receive response.completed event"
        assert completed_response is not None
        assert completed_response.usage is not None, "completed event must have usage"
        assert completed_response.usage.input_tokens > 0
        assert completed_response.usage.output_tokens > 0


# ---------------------------------------------------------------------------
# Responses API with reasoning + web_search
# ---------------------------------------------------------------------------

class TestVertexResponsesWebSearchWithReasoning:
    """Combine web_search with reasoning parameters (thinking models)."""

    @pytest.mark.parametrize("model", TestModels.VERTEX_MODELS_THINKING)
    def test_web_search_with_reasoning_effort(self, openai_client, model):
        """web_search + reasoning_effort should work together."""
        response = openai_client.responses.create(
            model=model,
            input="Search the web and tell me: what is the latest Python version?",
            tools=[{"type": "web_search_preview"}],
            reasoning={"effort": "low"},
            max_output_tokens=200,
        )
        validate_responses_api_response(response)
        validate_responses_api_usage(response)

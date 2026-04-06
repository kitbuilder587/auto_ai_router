"""Vertex AI tool-calling regression tests."""

import json

import pytest

from test_helpers import ContentValidator, TestModels


VERTEX_MODELS = TestModels.VERTEX_MODELS

WEATHER_TOOL = {
    "type": "function",
    "function": {
        "name": "get_weather",
        "description": "Returns the weather for the given city",
        "parameters": {
            "type": "object",
            "properties": {
                "city": {
                    "type": "string",
                    "enum": ["NY", "MSC"],
                }
            },
            "required": ["city"],
        },
    },
}


def get_weather(city: str) -> float:
    """Local stub used to answer tool calls."""
    if city == "NY":
        return 17.0
    if city == "MSC":
        return 15.3
    raise ValueError(f"unsupported city: {city}")


def validate_tool_call_response(response) -> list:
    """Validate the first model turn contains tool calls."""
    assert response is not None
    assert hasattr(response, "choices")
    assert len(response.choices) > 0

    message = response.choices[0].message
    assert message is not None
    assert message.tool_calls is not None
    assert len(message.tool_calls) == 2, f"expected 2 tool calls, got {message.tool_calls}"

    requested_cities = []
    for tool_call in message.tool_calls:
        assert tool_call.type == "function"
        assert tool_call.function.name == "get_weather"
        args = json.loads(tool_call.function.arguments)
        assert "city" in args
        requested_cities.append(args["city"])

    assert set(requested_cities) == {"NY", "MSC"}
    return list(message.tool_calls)


class TestVertexToolCall:
    """Vertex chat.completions tool calling."""

    @pytest.mark.parametrize("model", VERTEX_MODELS)
    def test_multiple_tool_calls_followup(self, openai_client, model):
        """Regression: multiple tool results after one assistant tool-call turn must work."""
        user_prompt = "Узнай погоду в Нью Йорке (NY) и Москве (MSC)"

        first_response = openai_client.chat.completions.create(
            model=model,
            temperature=0,
            messages=[{"role": "user", "content": user_prompt}],
            tools=[WEATHER_TOOL],
        )

        tool_calls = validate_tool_call_response(first_response)
        assistant_message = first_response.choices[0].message.model_dump(exclude_none=True)

        messages = [
            {"role": "user", "content": user_prompt},
            assistant_message,
        ]

        for tool_call in tool_calls:
            args = json.loads(tool_call.function.arguments)
            result = get_weather(**args)
            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": tool_call.id,
                    "content": str(result),
                }
            )

        final_response = openai_client.chat.completions.create(
            model=model,
            temperature=0,
            messages=messages,
        )

        final_text = final_response.choices[0].message.content
        assert final_text is not None
        ContentValidator.assert_contains_any(final_text, ["17", "17.0"])
        ContentValidator.assert_contains_any(final_text, ["15.3", "15,3"])
        ContentValidator.assert_contains_any(final_text, ["NY", "Нью-Й", "New York"])
        ContentValidator.assert_contains_any(final_text, ["MSC", "Моск", "Moscow"])

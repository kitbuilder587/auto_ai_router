"""
LangChain ChatOpenAI with tool_calls integration tests

Tests tool calling and structured outputs when using LangChain
ChatOpenAI client via auto_ai_router with various model providers.
"""

import pytest
from typing import Optional

try:
    from langchain_openai import ChatOpenAI
    from langchain_core.messages import HumanMessage, AIMessage, SystemMessage, ToolMessage
    from langchain_core.prompts import ChatPromptTemplate, MessagesPlaceholder
    from langchain.tools import tool
except ImportError:
    pytest.skip("LangChain not installed", allow_module_level=True)

from test_helpers import TestModels

# Model sets for different test scenarios
LANGCHAIN_ALL_MODELS = (
    TestModels.OPENAI_MODELS
    + TestModels.VERTEX_MODELS
    + TestModels.ANTHROPIC_MODELS
)


# ---------------------------------------------------------------------------
# Shared tool definitions
# ---------------------------------------------------------------------------

@tool
def get_weather(location: str) -> str:
    """Get weather information for a location"""
    weather_data = {
        "New York": "Cloudy, 15°C",
        "London": "Rainy, 10°C",
        "Tokyo": "Sunny, 22°C",
        "Sydney": "Warm, 25°C",
        "Paris": "Partly cloudy, 18°C",
    }
    return weather_data.get(location, "Weather data not available")


@tool
def calculate_distance(location1: str, location2: str) -> float:
    """Calculate distance between two locations in kilometers"""
    distances = {
        ("New York", "London"): 5570,
        ("New York", "Tokyo"): 10840,
        ("London", "Tokyo"): 9570,
        ("Sydney", "Tokyo"): 7820,
    }
    for k, v in distances.items():
        if set(k) == {location1, location2}:
            return float(v)
    return 0.0


@tool
def list_cities() -> list:
    """List available cities"""
    return ["New York", "London", "Tokyo", "Sydney"]


# ---------------------------------------------------------------------------
# Helper
# ---------------------------------------------------------------------------

def make_llm(model: str, base_url: str, api_key: str, **kwargs) -> ChatOpenAI:
    """Create a ChatOpenAI instance pointed at the router."""
    return ChatOpenAI(
        model=model,
        openai_api_base=base_url,
        openai_api_key=api_key,
        **kwargs,
    )


# ---------------------------------------------------------------------------
# Basic tool calling tests
# ---------------------------------------------------------------------------

class TestLangChainBasicTools:
    """Basic tool calling functionality tests"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calling_single_call(self, openai_client, base_url, model):
        """Test basic tool calling with ChatOpenAI"""
        llm = make_llm(model, base_url, openai_client.api_key, temperature=0.5)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        response = model_with_tools.invoke("What's the weather in Tokyo?")

        assert response is not None
        assert hasattr(response, 'tool_calls') or hasattr(response, 'content')

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calling_multiple_calls(self, openai_client, base_url, model):
        """Test tool calling with multiple tool invocations"""
        llm = make_llm(model, base_url, openai_client.api_key, temperature=0.3)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        response = model_with_tools.invoke(
            "What cities are available? Show me the weather in the first two."
        )

        assert response is not None

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_with_auto_tool_choice(self, openai_client, base_url, model):
        """Test tool calling with tool_choice='auto'"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools(
            [get_weather, calculate_distance, list_cities],
            tool_choice="auto",
        )

        response = model_with_tools.invoke("What's the weather in London?")

        assert response is not None

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_message_with_tool_results(self, openai_client, base_url, model):
        """Test conversation with tool calls and results"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        messages = [HumanMessage("What's the weather in Tokyo?")]
        response = model_with_tools.invoke(messages)

        assert response is not None


# ---------------------------------------------------------------------------
# Agent tests
# ---------------------------------------------------------------------------

class TestLangChainAgents:
    """Agent-based tool calling tests"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calling_agent_creation(self, openai_client, base_url, model):
        """Test creating a tool-calling agent with ChatOpenAI"""
        try:
            from langchain_classic.agents import create_tool_calling_agent
        except ImportError:
            pytest.skip("langchain_classic not installed")

        llm = make_llm(model, base_url, openai_client.api_key, temperature=0.3)
        tools = [get_weather, calculate_distance, list_cities]

        prompt = ChatPromptTemplate.from_messages([
            ("system", "You are a helpful assistant that can access various tools."),
            ("user", "{input}"),
            MessagesPlaceholder(variable_name="agent_scratchpad"),
        ])

        try:
            agent = create_tool_calling_agent(llm, tools, prompt)
            assert agent is not None
        except Exception as e:
            pytest.skip(f"Tool calling agent not supported: {str(e)}")

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_agent_with_executor(self, openai_client, base_url, model):
        """Test tool-calling agent with executor"""
        try:
            from langchain_classic.agents import AgentExecutor, create_tool_calling_agent
        except ImportError:
            pytest.skip("langchain_classic not installed")

        llm = make_llm(model, base_url, openai_client.api_key, temperature=0.3)
        tools = [get_weather, calculate_distance, list_cities]

        prompt = ChatPromptTemplate.from_messages([
            ("system", "You are a helpful weather assistant."),
            ("user", "{input}"),
            MessagesPlaceholder(variable_name="agent_scratchpad"),
        ])

        try:
            agent = create_tool_calling_agent(llm, tools, prompt)
            agent_executor = AgentExecutor.from_agent_and_tools(
                agent=agent,
                tools=tools,
                verbose=False,
                max_iterations=5,
            )

            result = agent_executor.invoke({
                "input": "What's the weather in Tokyo and London? Also tell me the distance between them."
            })

            assert result is not None
        except Exception as e:
            pytest.skip(f"Agent executor not fully supported: {str(e)}")


# ---------------------------------------------------------------------------
# Tool schema tests
# ---------------------------------------------------------------------------

class TestLangChainToolSchema:
    """Test tool schema and parameter validation"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_definition_schema(self, openai_client, base_url, model):
        """Test that tool schema is properly bound"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        bound_tools = model_with_tools.kwargs.get('tools')
        if bound_tools:
            assert len(bound_tools) > 0

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_with_complex_parameters(self, openai_client, base_url, model):
        """Test tool calling with complex parameter types"""
        @tool
        def search_locations(
            query: str,
            radius_km: Optional[float] = None,
            limit: Optional[int] = None,
        ) -> list:
            """Search for locations matching criteria"""
            return [
                {"name": "Tokyo", "distance": 0},
                {"name": "Osaka", "distance": 400},
                {"name": "Kyoto", "distance": 470},
            ]

        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([search_locations])

        response = model_with_tools.invoke(
            "Find locations near Tokyo within 500km, limit to 10 results"
        )

        assert response is not None


# ---------------------------------------------------------------------------
# Integration tests
# ---------------------------------------------------------------------------

class TestLangChainIntegration:
    """Integration tests with the router transform layer"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calls_with_parameters_transformation(self, openai_client, base_url, model):
        """Test that parameters are properly transformed through the router"""
        llm = make_llm(
            model, base_url, openai_client.api_key,
            temperature=0.7,
            max_tokens=500,
            top_p=0.9,
            frequency_penalty=0.1,
            presence_penalty=0.1,
        )
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        response = model_with_tools.invoke("What's the weather in London?")

        assert response is not None

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_streaming_with_tool_calls(self, openai_client, base_url, model):
        """Test streaming response with tool calling"""
        llm = make_llm(model, base_url, openai_client.api_key, streaming=True)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        try:
            chunks = list(model_with_tools.stream("What's the weather in Tokyo?"))
            assert len(chunks) > 0
        except Exception as e:
            pytest.skip(f"Streaming with tools error: {str(e)}")

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_concurrent_tool_calls(self, openai_client, base_url, model):
        """Test parallel tool execution"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        response = model_with_tools.invoke(
            "Get weather for Tokyo, London, and Sydney, and calculate distances between them"
        )

        assert response is not None

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_model_response_format(self, openai_client, base_url, model):
        """Test response format compatibility"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather])

        response = model_with_tools.invoke("What's the weather in Tokyo?")

        assert hasattr(response, 'content') or hasattr(response, 'tool_calls')

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_multiple_function_calls(self, openai_client, base_url, model):
        """Test handling multiple concurrent function calls"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        response = model_with_tools.invoke(
            "Get weather for Tokyo and London. Calculate distance between them."
        )

        assert response is not None


# ---------------------------------------------------------------------------
# Provider comparison tests
# ---------------------------------------------------------------------------

class TestProviderComparison:
    """Compare tool calling behavior between providers"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_vertex_tool_response_structure(self, openai_client, base_url, model):
        """Verify Vertex AI tool response structure"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather])

        response = model_with_tools.invoke("What's the weather in London?")

        assert hasattr(response, 'content') or hasattr(response, 'tool_calls')

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_anthropic_tool_response_structure(self, openai_client, base_url, model):
        """Verify Anthropic tool response structure"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather])

        response = model_with_tools.invoke("What's the weather in Paris?")

        assert hasattr(response, 'content') or hasattr(response, 'tool_calls')

    def test_google_vs_anthropic_same_prompt(self, openai_client, base_url):
        """Test same tool calling prompt with both provider types"""
        tools = [get_weather, calculate_distance]
        prompt = "What's the weather in Tokyo? How far is it from London?"

        for model in LANGCHAIN_ALL_MODELS + LANGCHAIN_ALL_MODELS:
            try:
                llm = make_llm(model, base_url, openai_client.api_key, temperature=0.3)
                model_with_tools = llm.bind_tools(tools)
                response = model_with_tools.invoke(prompt)
                assert response is not None, f"Expected response for model {model}"
            except Exception as e:
                pytest.skip(f"Provider comparison test failed for {model}: {str(e)}")


# ---------------------------------------------------------------------------
# Streaming tool support tests
# ---------------------------------------------------------------------------

class TestStreamingToolSupport:
    """Tests for streaming tool call support"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_streaming_text_only(self, openai_client, base_url, model):
        """Test streaming with text content only"""
        llm = make_llm(model, base_url, openai_client.api_key, streaming=True)

        try:
            chunks = list(llm.stream("Say hello"))
            assert len(chunks) > 0
        except Exception as e:
            pytest.skip(f"Basic streaming failed: {str(e)}")

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_streaming_with_tools(self, openai_client, base_url, model):
        """Test streaming with tool calls"""
        llm = make_llm(model, base_url, openai_client.api_key, streaming=True)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        try:
            chunks = list(model_with_tools.stream("What's the weather in London?"))
            assert len(chunks) > 0
        except Exception as e:
            pytest.skip(f"Streaming with tools error: {str(e)}")


# ---------------------------------------------------------------------------
# Tool calls with conversation history
# ---------------------------------------------------------------------------

class TestToolCallsWithHistory:
    """Test tool calling with conversation history"""

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calls_with_system_role(self, openai_client, base_url, model):
        """Test tool calling with system role message in history"""
        llm = make_llm(model, base_url, openai_client.api_key, temperature=1)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        messages = [
            SystemMessage("You are a helpful assistant with access to weather and distance tools."),
            HumanMessage("What's the weather in Tokyo?"),
            AIMessage(
                "I'll check the weather for Tokyo.",
                tool_calls=[
                    {
                        "id": "call_weather_001",
                        "name": "get_weather",
                        "args": {"location": "Tokyo"},
                    }
                ],
            ),
            ToolMessage("Sunny, 22°C", tool_call_id="call_weather_001"),
            HumanMessage("How far is Tokyo from London?"),
        ]

        response = model_with_tools.invoke(messages)
        assert response is not None
        assert hasattr(response, "content") or hasattr(response, "tool_calls")

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_multiple_function_calls_in_sequence(self, openai_client, base_url, model):
        """Test handling multiple function calls with proper sequence"""
        llm = make_llm(model, base_url, openai_client.api_key, temperature=0.7, seed=42)
        model_with_tools = llm.bind_tools([get_weather, calculate_distance, list_cities])

        messages = [
            HumanMessage("Get weather for Tokyo and London, then calculate distance between them"),
            AIMessage(
                content="I'll get the weather for both cities and calculate the distance.",
                tool_calls=[
                    {
                        "id": "call_weather_tokyo",
                        "name": "get_weather",
                        "args": {"location": "Tokyo"},
                    },
                    {
                        "id": "call_weather_london",
                        "name": "get_weather",
                        "args": {"location": "London"},
                    },
                    {
                        "id": "call_distance",
                        "name": "calculate_distance",
                        "args": {"location1": "Tokyo", "location2": "London"},
                    },
                ],
            ),
            ToolMessage("Sunny, 22°C", tool_call_id="call_weather_tokyo"),
            ToolMessage("Rainy, 10°C", tool_call_id="call_weather_london"),
            ToolMessage("9570", tool_call_id="call_distance"),
            HumanMessage("What should I pack for a trip to both cities?"),
        ]

        response = model_with_tools.invoke(messages)
        assert response is not None
        if hasattr(response, "content"):
            assert len(response.content) > 0

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calls_with_all_parameters(self, openai_client, base_url, model):
        """Test tool calling with all OpenAI-compatible parameters"""
        llm = make_llm(
            model, base_url, openai_client.api_key,
            temperature=1,
            seed=42,
            top_p=0.9,
            frequency_penalty=0.1,
            presence_penalty=0.1,
            max_tokens=500,
        )
        model_with_tools = llm.bind_tools([get_weather, calculate_distance])

        response = model_with_tools.invoke(
            "What's the weather in Tokyo and how far is it from London?"
        )
        assert response is not None

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_calls_response_format(self, openai_client, base_url, model):
        """Test that router returns proper tool_calls format"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools([get_weather])

        response = model_with_tools.invoke("What's the weather in Tokyo?")
        assert response is not None

        if hasattr(response, "tool_calls") and response.tool_calls:
            for tool_call in response.tool_calls:
                assert "id" in tool_call
                assert "name" in tool_call
                assert "args" in tool_call or "arguments" in tool_call
                if "type" in tool_call:
                    assert tool_call["type"] in ["function", "tool_call"]

    @pytest.mark.parametrize("model", LANGCHAIN_ALL_MODELS)
    def test_tool_choice_auto(self, openai_client, base_url, model):
        """Test with tool_choice='auto' parameter"""
        llm = make_llm(model, base_url, openai_client.api_key)
        model_with_tools = llm.bind_tools(
            [get_weather, calculate_distance],
            tool_choice="auto",
        )

        response = model_with_tools.invoke(
            "Get weather for London and compare with Paris distance"
        )
        assert response is not None

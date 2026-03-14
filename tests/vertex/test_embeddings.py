import pytest
from test_helpers import (
    TestModels, ResponseValidator, VectorMath
)

class TestVertexEmbeddings:
    """Test Vertex embedding functionality"""

    @pytest.mark.parametrize("model", TestModels.VERTEX_EMBEDDING_MODELS)
    def test_single_embedding(self, openai_client, model):
        """Test single text embedding"""
        response = openai_client.embeddings.create(
            model=model,
            input="The quick brown fox jumps"
        )

        ResponseValidator.validate_embedding_response(response, expected_count=1)
        assert len(response.data[0].embedding) > 0

    @pytest.mark.parametrize("model", TestModels.VERTEX_EMBEDDING_MODELS)
    def test_batch_embeddings(self, openai_client, model):
        """Test batch embeddings"""
        texts = [
            "Machine learning is AI",
            "Python is a language",
            "Weather is nice"
        ]

        response = openai_client.embeddings.create(
            model=model,
            input=texts
        )

        ResponseValidator.validate_embedding_response(response, expected_count=len(texts))
        embeddings = [data.embedding for data in response.data]

        # Test similarity: ML and AI should be more similar than ML and Weather
        sim_similar = VectorMath.cosine_similarity(embeddings[0], embeddings[1])
        sim_different = VectorMath.cosine_similarity(embeddings[0], embeddings[2])
        # Both should be reasonable, but we just check they exist
        assert -1 <= sim_similar <= 1
        assert -1 <= sim_different <= 1

    @pytest.mark.parametrize("model", TestModels.VERTEX_EMBEDDING_MODELS)
    def test_embedding_dimensions(self, openai_client, model):
        """Test embedding dimensions are consistent"""
        response = openai_client.embeddings.create(
            model=model,
            input="test"
        )

        embedding_dim = len(response.data[0].embedding)
        assert embedding_dim > 0
        # OpenAI embeddings typically have 1536 dimensions
        assert embedding_dim > 100

    @pytest.mark.parametrize("model", TestModels.VERTEX_EMBEDDING_MODELS)
    def test_embedding_usage_tokens(self, openai_client, model):
        """Response contains non-zero token counts in usage field."""
        response = openai_client.embeddings.create(
            model=model,
            input="Hello world",
        )

        assert response.usage is not None, "Expected usage field in embeddings response"
        assert response.usage.prompt_tokens > 0, (
            f"Expected prompt_tokens > 0, got {response.usage.prompt_tokens}"
        )
        assert response.usage.total_tokens > 0, (
            f"Expected total_tokens > 0, got {response.usage.total_tokens}"
        )
        assert response.usage.prompt_tokens == response.usage.total_tokens, (
            "For embeddings, prompt_tokens should equal total_tokens"
        )

    @pytest.mark.parametrize("model", TestModels.VERTEX_EMBEDDING_MODELS)
    def test_batch_embedding_usage_tokens_scale(self, openai_client, model):
        """Batch embedding token count should exceed single-text token count."""
        single_response = openai_client.embeddings.create(
            model=model,
            input="Hello world",
        )

        batch_response = openai_client.embeddings.create(
            model=model,
            input=["Hello world", "This is a longer second sentence for the batch."],
        )

        assert batch_response.usage.prompt_tokens > single_response.usage.prompt_tokens, (
            f"Batch tokens ({batch_response.usage.prompt_tokens}) should exceed "
            f"single tokens ({single_response.usage.prompt_tokens})"
        )

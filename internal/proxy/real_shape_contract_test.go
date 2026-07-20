package proxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/config"
	"github.com/mixaill76/auto_ai_router/internal/converter"
	"github.com/mixaill76/auto_ai_router/internal/litellmdb"
	"github.com/mixaill76/auto_ai_router/internal/models"
	"github.com/mixaill76/auto_ai_router/internal/shadowcontext"
	"github.com/mixaill76/auto_ai_router/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const realShapeConstraintsPath = "../../testdata/air-migration/real-shape-constraints-v2.json"

type realShapeConstraints struct {
	SchemaVersion string `json:"schema_version"`
	Provenance    struct {
		SanitizedCorpusSchemaVersion string `json:"sanitized_corpus_schema_version"`
		SanitizedCorpusSHA256        string `json:"sanitized_corpus_sha256"`
		SpendLogSourceSHA256         string `json:"spendlog_source_sha256"`
		SpendLogSourceComplete       bool   `json:"spendlog_source_complete"`
		WireMockSourceTreeSHA256     string `json:"wiremock_source_tree_sha256"`
		WireMockSourceRevision       string `json:"wiremock_source_revision"`
		WireMockProjectionSHA256     string `json:"wiremock_projection_sha256"`
		SpendLogDumpProfileSHA256    string `json:"spendlog_dump_profile_sha256"`
	} `json:"provenance"`
	EvidenceLimits struct {
		FullCorpusSHAIsBoundWithoutCandidatePackagingCycle   bool `json:"full_corpus_sha_is_bound_without_candidate_packaging_cycle"`
		NumericBoundsAreCombinedChatAndEmbeddingObservations bool `json:"numeric_bounds_are_combined_chat_and_embedding_observations"`
		NumericQuantilesAreNotCorrelatedRows                 bool `json:"numeric_quantiles_are_not_correlated_rows"`
		WireMockBehaviorIsNotAIRRuntimeResult                bool `json:"wiremock_behavior_is_not_an_air_runtime_result"`
		WireMockSourceQualityIsDegraded                      bool `json:"wiremock_source_quality_is_degraded"`
		WireMockShapesAreIndependentObservations             bool `json:"wiremock_shapes_are_independent_observations"`
		SpendLogTenantIdentityAttributionIsNotExposed        bool `json:"spendlog_tenant_identity_attribution_is_not_exposed"`
	} `json:"evidence_limits"`
	Privacy struct {
		ContainsRawSpendLogRows            bool `json:"contains_raw_spendlog_rows"`
		ContainsRawPromptsOrResponses      bool `json:"contains_raw_prompts_or_responses"`
		ContainsProductionModelOrProviders bool `json:"contains_production_model_or_provider_names"`
		ContainsTenantData                 bool `json:"contains_tenant_ids_keys_ips_or_timestamps"`
		OnlyAggregateOrShapeEvidence       bool `json:"only_aggregate_or_shape_evidence"`
	} `json:"privacy"`
	MigrationGate struct {
		BlockingOperations                 []string `json:"blocking_operations"`
		DecisionRequiredOperationsExcluded bool     `json:"decision_required_operations_are_excluded"`
		NativeAnthropicOperationsExcluded  bool     `json:"native_anthropic_operations_are_excluded"`
	} `json:"migration_gate"`
	RequestTransport struct {
		MaxObservedBodyBytes int `json:"max_observed_body_bytes"`
		MaxObservedJSONDepth int `json:"max_observed_json_depth"`
		MaxObservedJSONNodes int `json:"max_observed_json_nodes"`
	} `json:"request_transport"`
	WireMockShapes struct {
		ChatStreamDoneTerminal           bool  `json:"chat_stream_done_terminal"`
		ChatStreamDataEventCount         int   `json:"chat_stream_data_event_count"`
		ChatHasToolCallShape             bool  `json:"chat_has_tool_call_shape"`
		ChatHasReasoningTokenDetailShape bool  `json:"chat_has_reasoning_token_detail_shape"`
		EmbeddingDimensions              []int `json:"embedding_dimensions"`
		AuthenticatedModelListCount      int   `json:"authenticated_model_list_count"`
	} `json:"wiremock_shapes"`
	SpendLogShapes struct {
		ClosedRowCount          int `json:"closed_row_count"`
		ChatRowCount            int `json:"chat_row_count"`
		EmbeddingRowCount       int `json:"embedding_row_count"`
		TokenTotalMismatchCount int `json:"token_total_mismatch_count"`
		NegativeDurationCount   int `json:"negative_duration_count"`
		NegativeSpendCount      int `json:"negative_spend_count"`
		PromptTokens            struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"prompt_tokens"`
		CompletionTokens struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"completion_tokens"`
		TotalTokens struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"total_tokens"`
		DurationMS struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"duration_ms"`
		TimeToFirstOutputMS struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"time_to_first_output_ms"`
		SpendDecimal struct {
			Min string `json:"min"`
			Max string `json:"max"`
		} `json:"spend_decimal"`
	} `json:"spendlog_shapes"`
}

func loadRealShapeConstraints(t *testing.T) realShapeConstraints {
	t.Helper()
	raw, err := os.ReadFile(realShapeConstraintsPath)
	require.NoError(t, err)
	var constraints realShapeConstraints
	require.NoError(t, json.Unmarshal(raw, &constraints))
	require.Equal(t, "air-migration-real-shape-constraints/v2", constraints.SchemaVersion)
	for name, digest := range map[string]string{
		"spendlog source":       constraints.Provenance.SpendLogSourceSHA256,
		"spendlog dump profile": constraints.Provenance.SpendLogDumpProfileSHA256,
		"wiremock projection":   constraints.Provenance.WireMockProjectionSHA256,
		"wiremock source tree":  constraints.Provenance.WireMockSourceTreeSHA256,
		"sanitized corpus":      constraints.Provenance.SanitizedCorpusSHA256,
	} {
		decoded, decodeErr := hex.DecodeString(digest)
		require.NoError(t, decodeErr, name)
		require.Len(t, decoded, 32, name)
	}
	require.Equal(t, "air-migration-real-shape-corpus/v2", constraints.Provenance.SanitizedCorpusSchemaVersion)
	require.True(t, constraints.EvidenceLimits.FullCorpusSHAIsBoundWithoutCandidatePackagingCycle)
	require.True(t, constraints.EvidenceLimits.NumericBoundsAreCombinedChatAndEmbeddingObservations)
	require.True(t, constraints.EvidenceLimits.NumericQuantilesAreNotCorrelatedRows)
	require.True(t, constraints.EvidenceLimits.WireMockBehaviorIsNotAIRRuntimeResult)
	require.True(t, constraints.EvidenceLimits.WireMockSourceQualityIsDegraded)
	require.True(t, constraints.EvidenceLimits.WireMockShapesAreIndependentObservations)
	require.True(t, constraints.EvidenceLimits.SpendLogTenantIdentityAttributionIsNotExposed)
	require.False(t, constraints.Provenance.SpendLogSourceComplete)
	require.False(t, constraints.Privacy.ContainsRawSpendLogRows)
	require.False(t, constraints.Privacy.ContainsRawPromptsOrResponses)
	require.False(t, constraints.Privacy.ContainsProductionModelOrProviders)
	require.False(t, constraints.Privacy.ContainsTenantData)
	require.True(t, constraints.Privacy.OnlyAggregateOrShapeEvidence)
	require.Equal(t, []string{
		"POST /v1/chat/completions",
		"POST /v1/embeddings",
		"GET /v1/models",
	}, constraints.MigrationGate.BlockingOperations)
	require.True(t, constraints.MigrationGate.DecisionRequiredOperationsExcluded)
	require.True(t, constraints.MigrationGate.NativeAnthropicOperationsExcluded)
	return constraints
}

func TestSyntheticChatAndEmbeddingTransportAtIndependentObservedBounds(t *testing.T) {
	constraints := loadRealShapeConstraints(t)
	paths := []struct {
		name      string
		path      string
		dimension int
	}{
		{name: "chat", path: "/v1/chat/completions"},
	}
	for _, dimension := range constraints.WireMockShapes.EmbeddingDimensions {
		paths = append(paths, struct {
			name      string
			path      string
			dimension int
		}{name: fmt.Sprintf("embedding-%d", dimension), path: "/v1/embeddings", dimension: dimension})
	}

	for _, tc := range paths {
		for _, stress := range []string{"body_bytes", "json_depth", "json_nodes"} {
			t.Run(tc.name+"/"+stress, func(t *testing.T) {
				body := makeSyntheticIndependentBoundaryRequest(t, tc.path, stress, constraints)
				received := make(chan []byte, 1)
				upstream := newIPv4Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, readErr := io.ReadAll(r.Body)
					if readErr != nil {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					received <- raw
					w.Header().Set("Content-Type", "application/json")
					if tc.path == "/v1/chat/completions" {
						_ = json.NewEncoder(w).Encode(createMockChatCompletionResponse("fixture-response", "fixture-model", "ok"))
						return
					}
					embedding := make([]float64, tc.dimension)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"object": "list",
						"model":  "fixture-model",
						"data": []any{map[string]any{
							"object": "embedding", "index": 0, "embedding": embedding,
						}},
						"usage": map[string]int{"prompt_tokens": 6, "total_tokens": 6},
					})
				}))
				defer upstream.Close()

				manager := models.New(testhelpers.NewTestLogger(), 50, nil)
				prx := buildProxyBodyTestProxyWithMM(upstream.URL, manager)
				req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer master-key")
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				prx.ProxyRequest(w, req)

				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				select {
				case upstreamBody := <-received:
					assert.Equal(t, body, upstreamBody)
				case <-time.After(time.Second):
					t.Fatal("provider did not receive the request")
				}
				if tc.dimension > 0 {
					var response struct {
						Data []struct {
							Embedding []float64 `json:"embedding"`
						} `json:"data"`
					}
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
					require.Len(t, response.Data, 1)
					assert.Len(t, response.Data[0].Embedding, tc.dimension)
				}
			})
		}
	}
}

// makeSyntheticIndependentBoundaryRequest stresses one observed aggregate
// maximum at a time. The privacy-safe profile does not preserve row-level
// correlation, so combining all p100 values into an "observed request" would
// claim evidence that the dump intentionally does not expose.
func makeSyntheticIndependentBoundaryRequest(t *testing.T, path, stress string, constraints realShapeConstraints) []byte {
	t.Helper()
	request := map[string]any{
		"model": "fixture-model",
	}
	if path == "/v1/chat/completions" {
		request["messages"] = []any{map[string]any{"role": "user", "content": "fixture"}}
		request["tools"] = []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "fixture_tool",
				"parameters": map[string]any{"type": "object"},
			},
		}}
	} else {
		request["input"] = "fixture"
	}

	switch stress {
	case "body_bytes":
		request["padding"] = ""
		raw, err := json.Marshal(request)
		require.NoError(t, err)
		paddingBytes := constraints.RequestTransport.MaxObservedBodyBytes - len(raw)
		require.Positive(t, paddingBytes)
		request["padding"] = strings.Repeat("x", paddingBytes)
	case "json_depth":
		var nested any = "leaf"
		for range constraints.RequestTransport.MaxObservedJSONDepth - 1 {
			nested = map[string]any{"next": nested}
		}
		request["metadata"] = nested
	case "json_nodes":
		request["node_padding"] = []string{}
		baseNodes := realJSONNodes(request)
		paddingNodes := constraints.RequestTransport.MaxObservedJSONNodes - baseNodes
		require.Positive(t, paddingNodes)
		request["node_padding"] = make([]string, paddingNodes)
	default:
		require.FailNow(t, "unknown independent transport stress", stress)
	}

	raw, err := json.Marshal(request)
	require.NoError(t, err)

	var decoded any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	depth := realJSONDepth(decoded)
	nodes := realJSONNodes(decoded)
	assert.LessOrEqual(t, len(raw), constraints.RequestTransport.MaxObservedBodyBytes)
	assert.LessOrEqual(t, depth, constraints.RequestTransport.MaxObservedJSONDepth)
	assert.LessOrEqual(t, nodes, constraints.RequestTransport.MaxObservedJSONNodes)
	switch stress {
	case "body_bytes":
		assert.Len(t, raw, constraints.RequestTransport.MaxObservedBodyBytes)
	case "json_depth":
		assert.Equal(t, constraints.RequestTransport.MaxObservedJSONDepth, depth)
	case "json_nodes":
		assert.Equal(t, constraints.RequestTransport.MaxObservedJSONNodes, nodes)
	}
	return raw
}

func realJSONDepth(value any) int {
	maxChild := 0
	switch current := value.(type) {
	case map[string]any:
		for _, child := range current {
			maxChild = max(maxChild, realJSONDepth(child))
		}
		return 1 + maxChild
	case []any:
		for _, child := range current {
			maxChild = max(maxChild, realJSONDepth(child))
		}
		return 1 + maxChild
	default:
		return 0
	}
}

func realJSONNodes(value any) int {
	nodes := 1
	switch current := value.(type) {
	case map[string]any:
		for _, child := range current {
			nodes += realJSONNodes(child)
		}
	case []any:
		for _, child := range current {
			nodes += realJSONNodes(child)
		}
	}
	return nodes
}

func TestSyntheticStreamCombinesIndependentWireMockShapesWithoutChangingTerminalSemantics(t *testing.T) {
	constraints := loadRealShapeConstraints(t)
	require.True(t, constraints.WireMockShapes.ChatStreamDoneTerminal)
	require.True(t, constraints.WireMockShapes.ChatHasToolCallShape)
	require.True(t, constraints.WireMockShapes.ChatHasReasoningTokenDetailShape)
	require.GreaterOrEqual(t, constraints.WireMockShapes.ChatStreamDataEventCount, 2)

	providerModel := "fixture-provider-model"
	publicModel := "fixture-public-model"
	events := make([]map[string]any, 0, constraints.WireMockShapes.ChatStreamDataEventCount)
	events = append(events, map[string]any{
		"id": "fixture-stream", "model": providerModel,
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "fixture-tool-call", "type": "function",
				"function": map[string]any{"name": "fixture_tool", "arguments": "{}"},
			}}},
		}},
	})
	for len(events) < constraints.WireMockShapes.ChatStreamDataEventCount-1 {
		events = append(events, map[string]any{
			"id": "fixture-stream", "model": providerModel,
			"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"content": "x"},
			}},
		})
	}
	events = append(events, map[string]any{
		"id": "fixture-stream", "model": providerModel, "choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":             constraints.SpendLogShapes.PromptTokens.Min,
			"completion_tokens":         1,
			"total_tokens":              constraints.SpendLogShapes.PromptTokens.Min + 1,
			"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
		},
	})

	var source strings.Builder
	for _, event := range events {
		raw, err := json.Marshal(event)
		require.NoError(t, err)
		source.WriteString("data: ")
		source.Write(raw)
		source.WriteString("\n\n")
	}
	source.WriteString("data: [DONE]\n\n")
	logCtx := &RequestLogContext{
		Billing: NewBillingContext("fixture-event", "fixture-call", "/v1/chat/completions", shadowcontext.Identity{}).
			WithPublicModel(publicModel),
	}

	normalized, err := io.ReadAll(normalizeSuccessfulResponseModelStream(
		iotest.OneByteReader(strings.NewReader(source.String())),
		http.StatusOK,
		logCtx,
		providerModel,
	))
	require.NoError(t, err)
	output := string(normalized)
	assert.Equal(t, constraints.WireMockShapes.ChatStreamDataEventCount, strings.Count(output, "data: {"))
	assert.True(t, strings.HasSuffix(output, "data: [DONE]\n\n"))
	assert.Contains(t, output, `"model":"`+publicModel+`"`)
	assert.NotContains(t, output, `"model":"`+providerModel+`"`)
	assert.Contains(t, output, `"tool_calls"`)
	assert.Contains(t, output, `"reasoning_tokens":1`)

	usage := extractTokenUsageFromStreamingChunk(output)
	require.NotNil(t, usage)
	assert.Equal(t, constraints.SpendLogShapes.PromptTokens.Min, usage.PromptTokens)
	assert.Equal(t, 1, usage.CompletionTokens)
	assert.Equal(t, 1, usage.ReasoningTokens)
}

func TestSyntheticBillingContractAtIndependentObservedTokenAndDurationBounds(t *testing.T) {
	constraints := loadRealShapeConstraints(t)
	require.Equal(t, constraints.SpendLogShapes.ClosedRowCount,
		constraints.SpendLogShapes.ChatRowCount+constraints.SpendLogShapes.EmbeddingRowCount)
	require.Zero(t, constraints.SpendLogShapes.TokenTotalMismatchCount)
	require.Zero(t, constraints.SpendLogShapes.NegativeDurationCount)
	require.Zero(t, constraints.SpendLogShapes.NegativeSpendCount)

	const inputRate = 0.0000001
	const outputRate = 0.0000004
	registry := models.NewModelPriceRegistry()
	registry.Update(map[string]*models.ModelPrice{
		"fixture-priced-model": {
			InputCostPerToken:  inputRate,
			OutputCostPerToken: outputRate,
		},
	})
	prx := &Proxy{logger: slog.New(slog.DiscardHandler), priceRegistry: registry}
	// Tenant identity is deliberately absent from the privacy-safe dump. Keep
	// exercising AIR's existing identity-integrity contract with synthetic data
	// without presenting these values as observations from the dump.
	identity := shadowcontext.Identity{
		APIKeyHash:     strings.Repeat("a", 64),
		UserID:         "fixture-user",
		TeamID:         "fixture-team",
		OrganizationID: "fixture-organization",
		ProjectID:      "fixture-project",
		AgentID:        "fixture-agent",
		PublicModel:    "fixture-public-model",
		DeploymentID:   "fixture-deployment",
		EndUser:        "fixture-end-user",
		Tags:           []string{"fixture-tag"},
		CallID:         "fixture-call",
	}

	for _, tc := range []struct {
		name             string
		endpoint         string
		callType         RouteKind
		promptTokens     int
		completionTokens int
	}{
		{
			name:             "chat",
			endpoint:         "/v1/chat/completions",
			callType:         RouteCompletion,
			promptTokens:     constraints.SpendLogShapes.TotalTokens.Max - constraints.SpendLogShapes.CompletionTokens.Max,
			completionTokens: constraints.SpendLogShapes.CompletionTokens.Max,
		},
		{
			name:         "embedding",
			endpoint:     "/v1/embeddings",
			callType:     RouteEmbedding,
			promptTokens: constraints.SpendLogShapes.PromptTokens.Max,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.GreaterOrEqual(t, tc.promptTokens, constraints.SpendLogShapes.PromptTokens.Min)
			require.LessOrEqual(t, tc.promptTokens, constraints.SpendLogShapes.PromptTokens.Max)
			require.GreaterOrEqual(t, tc.completionTokens, constraints.SpendLogShapes.CompletionTokens.Min)
			require.LessOrEqual(t, tc.completionTokens, constraints.SpendLogShapes.CompletionTokens.Max)
			expectedTotal := tc.promptTokens + tc.completionTokens
			require.GreaterOrEqual(t, expectedTotal, constraints.SpendLogShapes.TotalTokens.Min)
			require.LessOrEqual(t, expectedTotal, constraints.SpendLogShapes.TotalTokens.Max)

			start := time.Now().Add(-time.Duration(constraints.SpendLogShapes.DurationMS.Max) * time.Millisecond)
			completionStart := start.Add(time.Duration(constraints.SpendLogShapes.TimeToFirstOutputMS.Max) * time.Millisecond)
			billing := NewBillingContext("fixture-event", "fixture-call", tc.endpoint, identity).
				WithRouting("fixture-backend-model", "fixture-priced-model", "fixture-provider", "fixture-credential", "https://fixture.invalid/v1")
			request := httptest.NewRequest(http.MethodPost, tc.endpoint, nil)
			entry := prx.buildSpendEntry(&RequestLogContext{
				RequestID:           "fixture-event",
				CallID:              "fixture-call",
				ShadowContext:       shadowcontext.Result{State: shadowcontext.StateValid, Identity: identity},
				Billing:             billing,
				TokenInfo:           &litellmdb.TokenInfo{KeyAlias: "fixture-key-alias"},
				StartTime:           start,
				CompletionStartTime: completionStart,
				Request:             request,
				Status:              "success",
				HTTPStatus:          http.StatusOK,
				TokenUsage:          &converter.TokenUsage{PromptTokens: tc.promptTokens, CompletionTokens: tc.completionTokens},
				UsageSource:         "provider",
				Credential:          &config.CredentialConfig{Name: "fixture-credential", Type: config.ProviderTypeProxy},
				ModelID:             "fixture-backend-model",
				RealModelID:         "fixture-priced-model",
				DeclaredToolNames:   []string{"fixture_tool"},
			})

			require.NotNil(t, entry)
			assert.Equal(t, string(tc.callType), entry.CallType)
			assert.Equal(t, tc.promptTokens, entry.PromptTokens)
			assert.Equal(t, tc.completionTokens, entry.CompletionTokens)
			assert.Equal(t, expectedTotal, entry.TotalTokens)
			assert.Equal(t, identity.APIKeyHash, entry.APIKey)
			assert.Equal(t, identity.UserID, entry.UserID)
			assert.Equal(t, identity.TeamID, entry.TeamID)
			assert.Equal(t, identity.OrganizationID, entry.OrganizationID)
			assert.Equal(t, identity.ProjectID, entry.ProjectID)
			assert.Equal(t, identity.AgentID, entry.AgentID)
			assert.Equal(t, identity.EndUser, entry.EndUser)
			assert.Equal(t, identity.PublicModel, entry.ModelGroup)
			assert.Equal(t, identity.DeploymentID, entry.ModelID)
			assert.JSONEq(t, `["fixture-tag"]`, entry.RequestTags)
			assert.Equal(t, []string{"fixture_tool"}, entry.DeclaredToolNames)
			assert.Equal(t, "fixture-key-alias", entry.ToolKeyAlias)
			assert.True(t, entry.ComparisonEligible)
			require.NotNil(t, entry.CompletionStartTime)
			assert.Equal(t, completionStart, *entry.CompletionStartTime)
			assert.GreaterOrEqual(t, entry.RequestDurationMS, constraints.SpendLogShapes.DurationMS.Max)
			assert.Less(t, entry.RequestDurationMS, constraints.SpendLogShapes.DurationMS.Max+2000)

			expectedCost := float64(tc.promptTokens)*inputRate + float64(tc.completionTokens)*outputRate
			assert.InDelta(t, expectedCost, entry.Spend, 1e-12)
		})
	}
}

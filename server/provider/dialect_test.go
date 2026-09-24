package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/types"
)

// testDialectProvider is what every dialect test drives: build the request,
// parse the result.
type testDialectProvider interface {
	engine.Provider
	build(state *RequestState) (*openai.CompletionRequest, error)
	parse(state *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error)
}

func mustBuildProvider(role Role, dialectName string, cfg *types.ProviderConfig) testDialectProvider {
	prov, err := Build(role, dialectName, cfg)
	if err != nil {
		panic(err)
	}
	built, ok := prov.(testDialectProvider)
	if !ok {
		panic("provider " + dialectName + " does not expose dialect build/parse")
	}
	return built
}

func buildPromptForTest(p testDialectProvider, state *RequestState) *openai.CompletionRequest {
	req, err := p.build(state)
	if err != nil {
		panic(err)
	}
	return req
}

func parseCompletionForTest(p testDialectProvider, state *RequestState, result *openai.CompletionResult) *types.CompletionResponse {
	resp, err := p.parse(state, result)
	if err != nil {
		panic(err)
	}
	return resp
}

func stateForInput(input sourcectx.CompletionInput) *RequestState {
	return &RequestState{
		Input:  input,
		Window: RequestWindow{Lines: input.Current.File.Lines, CursorLine: input.Current.Cursor.Row - 1},
	}
}

func TestDialectsList(t *testing.T) {
	expected := []string{"edit-sweep", "edit-zeta2", "edit-zeta21", "fim-mellum", "fim-plain", "fim-qwen"}
	assert.Equal(t, expected, Dialects(), "dialect names")
}

func TestBuildRejectsUnknownDialect(t *testing.T) {
	_, err := Build(RoleType, "not-a-dialect", &types.ProviderConfig{})
	assert.Error(t, err, "unknown dialect rejected")
}

func TestBuildResolvesFamilyPresetFromModelName(t *testing.T) {
	mellum := mustBuildProvider(RoleType, "", &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "mellum-4b-dpo-all.Q8_0"},
	})
	mellumTokensExpected := mellumTokens()
	emptyWindow := stateForInput(sourcectx.CompletionInput{})
	req := buildPromptForTest(mellum, emptyWindow)
	// Current-file header, then the suffix-first envelope from the preset.
	expected := mellumTokensExpected.Filename + "\n" +
		mellumTokensExpected.Suffix + mellumTokensExpected.Prefix + mellumTokensExpected.Middle
	assert.Equal(t, expected, req.Prompt, "mellum preset with suffix-first order")

	qwen := mustBuildProvider(RoleType, "", &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "Qwen3.5-0.8B-Q8_0"},
	})
	qwenTokensExpected := qwenTokens()
	req = buildPromptForTest(qwen, emptyWindow)
	// Repo header for the workspace fallback, current-file header, then the
	// prefix-first envelope from the preset.
	expected = qwenTokensExpected.RepoName + "repo\n" + qwenTokensExpected.FileSep + "\n" +
		qwenTokensExpected.Prefix + qwenTokensExpected.Suffix + qwenTokensExpected.Middle
	assert.Equal(t, expected, req.Prompt, "qwen preset with prefix-first order")
}

func TestBuildRejectsAutoResolvedRoleMismatch(t *testing.T) {
	_, err := Build(RoleEdit, "", &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "mellum-4b"},
	})
	assert.Error(t, err, "type model rejected for edit role")

	_, err = Build(RoleType, "", &types.ProviderConfig{
		Endpoint: types.EndpointConfig{Model: "zeta-2.1.Q8_0"},
	})
	assert.Error(t, err, "edit model rejected for type role")
}

func TestBuildPinnedDialectBypassesRoleCheck(t *testing.T) {
	p, err := Build(RoleType, "edit-sweep", &types.ProviderConfig{})
	assert.NoError(t, err, "pinned dialect ignores the family role")
	if prov, ok := p.(*streamingProvider); ok {
		assert.Equal(t, engine.CompletionFIM, prov.CompletionKind(), "role decides the completion kind")
	} else {
		t.Fatal("pinned edit-sweep dialect should stream")
	}
}

func TestBuildAppliesRoleMaxTokenDefaults(t *testing.T) {
	typeReq := buildPromptForTest(
		mustBuildProvider(RoleType, "fim-plain", &types.ProviderConfig{}),
		stateForInput(sourcectx.CompletionInput{}),
	)
	assert.Equal(t, defaultTypeMaxTokens, typeReq.MaxTokens, "type role default")

	editReq := buildPromptForTest(
		mustBuildProvider(RoleEdit, "edit-zeta21", &types.ProviderConfig{}),
		stateForInput(sourcectx.CompletionInput{}),
	)
	assert.Equal(t, defaultEditMaxTokens, editReq.MaxTokens, "edit role default")
}

func TestResolveModelPrefersRoleFamiliesInOrder(t *testing.T) {
	typeOrder := &Resolved{Models: []ModelInfo{
		{ID: "zed_zeta-2-Q5_K_M"},
		{ID: "Qwen3.5-0.8B-Q8_0"},
		{ID: "mellum-4b-dpo-all.Q8_0"},
	}}
	assert.Equal(t, "mellum-4b-dpo-all.Q8_0", ResolveModel(RoleType, typeOrder), "mellum wins for type role")

	editOrder := &Resolved{Models: []ModelInfo{
		{ID: "sweep-next-edit-v2-7B"},
		{ID: "zed_zeta-2-Q5_K_M"},
		{ID: "zeta-2.1.Q8_0"},
	}}
	assert.Equal(t, "zeta-2.1.Q8_0", ResolveModel(RoleEdit, editOrder), "zeta-2.1 wins for edit role")
}

func TestResolveModelFallsBackToPlainAndSkipsWrongRole(t *testing.T) {
	plainOnly := &Resolved{Models: []ModelInfo{{ID: "llama-3.1-8b-instruct"}}}
	assert.Equal(t, "llama-3.1-8b-instruct", ResolveModel(RoleType, plainOnly), "plain fallback")

	editOnly := &Resolved{Models: []ModelInfo{{ID: "sweep-next-edit-v2-7B"}}}
	assert.Equal(t, "", ResolveModel(RoleType, editOnly), "edit models never serve the type role")
	assert.Equal(t, "sweep-next-edit-v2-7B", ResolveModel(RoleEdit, editOnly), "sweep resolves for edit")

	assert.Equal(t, "", ResolveModel(RoleEdit, nil), "nil resolution is empty")
}

func TestProbeReadsModelsContextAndFIMVocab(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "mellum-4b", "status": "loaded"},
				{"id": "Qwen3.5-0.8B", "status": "sleeping"},
				{"id": "my-codestral", "status": map[string]any{"value": "unloaded", "args": []any{"--model"}, "preset": ""}},
			}})
		case "/props":
			// Routers mask n_ctx with 0; probe falls back to the standard window.
			json.NewEncoder(w).Encode(map[string]any{"default_generation_settings": map[string]any{"n_ctx": 0}})
		case "/tokenize":
			var req struct {
				Content string `json:"content"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			// Mellum's tags are absent from this vocab; everything else is a
			// single special token.
			switch req.Content {
			case "<fim_prefix>", "<fim_suffix>", "<fim_middle>", "<filename>":
				json.NewEncoder(w).Encode(map[string]any{"tokens": []int{1, 2, 3}})
			default:
				json.NewEncoder(w).Encode(map[string]any{"tokens": []int{42}})
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	res, err := Probe(context.Background(), &types.EndpointConfig{URL: server.URL})
	assert.NoError(t, err, "probe")
	assert.Equal(t, 3, len(res.Models), "model count")
	assert.Equal(t, "loaded", res.Models[0].State, "state read from /v1/models")
	assert.Equal(t, "unloaded", res.Models[2].State, "router object status normalizes to its value")
	assert.Equal(t, fallbackContextSize, res.ContextSize, "masked n_ctx falls back to 8192")

	expectedMellum := mellumTokens()
	mellum := res.FIMTokens["mellum-4b"]
	assert.NotNil(t, mellum, "name-matched mellum keeps its preset despite an absent vocab")
	if mellum != nil {
		assert.Equal(t, expectedMellum.Prefix, mellum.Prefix, "mellum family preset")
	}

	expectedQwen := qwenTokens()
	detected := res.FIMTokens["my-codestral"]
	assert.NotNil(t, detected, "unknown model detected as qwen by vocab")
	if detected != nil {
		assert.Equal(t, expectedQwen.RepoName, detected.RepoName, "qwen family preset")
	}

	nameMatched := res.FIMTokens["Qwen3.5-0.8B"]
	assert.NotNil(t, nameMatched, "name-matched qwen keeps its preset")
}

func TestProbeToleratesPropsAndTokenizeOutages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "mellum-4b"},
				{"id": "some-plain-model"},
			}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	res, err := Probe(context.Background(), &types.EndpointConfig{URL: server.URL})
	assert.NoError(t, err, "models still return a usable result")
	assert.Equal(t, 2, len(res.Models), "partial results kept")
	assert.Equal(t, 0, res.ContextSize, "unanswered props stays unknown")
	assert.NotNil(t, res.FIMTokens["mellum-4b"], "name-matched family falls back to its preset")
	_, ok := res.FIMTokens["some-plain-model"]
	assert.False(t, ok, "unknown name with dead tokenize gets no preset")
}

func TestProbeFailsWithoutModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := Probe(context.Background(), &types.EndpointConfig{URL: server.URL})
	assert.Error(t, err, "models failure is fatal")
}

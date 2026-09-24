package ctx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cursortab/assert"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
	"cursortab/utils"
)

type transportSetter interface {
	SetHTTPTransport(rt http.RoundTripper)
}

type liveBuffer struct{}

func (liveBuffer) Diagnostics() *types.Diagnostics                          { return nil }
func (liveBuffer) TreesitterSymbols(int, int, int) *types.TreesitterContext { return nil }

type captureTransport struct {
	base       http.RoundTripper
	bodyLen    *int
	respPrefix *string
}

func (c captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := req.GetBody()
	buf, _ := io.ReadAll(body)
	*c.bodyLen = len(buf)
	resp, err := c.base.RoundTrip(req)
	if err == nil && resp.Body != nil {
		respBuf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		*c.respPrefix = string(respBuf)
		resp.Body = io.NopCloser(strings.NewReader(string(respBuf)))
	}
	return resp, err
}

type liveResponse struct {
	Choices []struct {
		Text         string `json:"text"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

func TestLiveLlamacppPromptStaysUnderContextSize(t *testing.T) {
	baseURL := os.Getenv("LIVE_LLAMACPP")
	if baseURL == "" {
		t.Skip("set LIVE_LLAMACPP=<base url> to run against a real llama.cpp server")
	}
	config := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{
			URL:       baseURL,
			Model:     "Qwen2.5-Coder-7B.Q8_0",
			MaxTokens: 64,
		},
		CompletionPath: "/v1/completions",
		ContextSize:    8192,
		Temperature:    0,
		FIMTokens: &types.FIMTokenConfig{
			Prefix:   "<|fim_prefix|>",
			Suffix:   "<|fim_suffix|>",
			Middle:   "<|fim_middle|>",
			RepoName: "<|repo_name|>",
			FileSep:  "<|file_sep|>",
		},
	}
	prov, err := provider.Build(provider.RoleType, "fim-qwen", config)
	assert.NoError(t, err, "build fim-qwen provider")

	var bodyLen int
	var respPrefix string
	prov.(transportSetter).SetHTTPTransport(captureTransport{base: http.DefaultTransport, bodyLen: &bodyLen, respPrefix: &respPrefix})

	currentLines := make([]string, 2000)
	for i := range currentLines {
		currentLines[i] = fmt.Sprintf("func helper%d(items []string, cache map[string]int) (int, error) {", i)
	}
	recentLines := make([]string, 30)
	for i := range recentLines {
		recentLines[i] = fmt.Sprintf("// section %d: handles parsing of buffered records and retries", i)
	}
	snapshot := sourcectx.FileContextSnapshot{NowNs: time.Now().UnixNano()}
	for _, name := range []string{"one.go", "two.go", "three.go", "four.go", "five.go"} {
		snapshot.RecentFiles = append(snapshot.RecentFiles, sourcectx.RecentFileSnapshot{
			Path:       name,
			FirstLines: recentLines,
		})
	}

	current := sourcectx.CurrentSnapshot{
		WorkspacePath: "/tmp/repo",
		File:          sourcectx.FileSnapshot{Path: "current.go", Lines: currentLines},
		Cursor:        sourcectx.CursorPosition{Row: 1000, Col: 40},
	}
	sourceInput := sourcectx.ContextSourceInput{
		Current:  current,
		Snapshot: snapshot,
		Buffer:   liveBuffer{},
		Limits: sourcectx.CollectionLimits{
			MaxSiblings:        50,
			MaxDiffBytes:       4096,
			MaxChangedSymbols:  50,
			MaxRecentSnapshots: 3,
			MaxRecentFileBytes: 4096,
			MaxDiffTokens:      512,
			ContextChars:       prov.MaterialsBudgetChars(),
		},
	}

	collected, used := sourcectx.Collect(context.Background(), sourceInput, prov.RequiredMaterials())
	input := sourcectx.CompletionInput{Current: current, Materials: collected, ContextChars: used}

	reqCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, err = prov.Complete(reqCtx, input)
	assert.NoError(t, err, "complete against live server")

	var live liveResponse
	assert.NoError(t, json.Unmarshal([]byte(respPrefix), &live), "parse live response")
	assert.True(t, live.Usage.PromptTokens <= config.ContextSize-config.Endpoint.MaxTokens,
		"server-tokenized prompt stays under the input budget")
	assert.NotEqual(t, "", live.Choices[0].FinishReason, "server finished the request")

	budgetChars := utils.EstimateCharsFromTokens(provider.InputTokens(config))
	// JSON escaping roughly doubles newlines and quotes; keep a margin.
	assert.True(t, bodyLen < budgetChars+4000, "request body stays near the token budget")

	t.Logf("materials chars: %d, request body bytes: %d, input token budget chars: %d", used, bodyLen, budgetChars)
	t.Logf("response prefix: %.600s", respPrefix)
}

package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"cursortab/client/openai"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
)

// OpenAI is the shared transport for every dialect: config-derived request
// fields come from the provider config, prompt/suffix/stop from the dialect
// build.
type OpenAI struct {
	config *types.ProviderConfig
	name   string
	path   string
	client *openai.Client
}

func NewOpenAI(name string, config *types.ProviderConfig) OpenAI {
	path := config.CompletionPath
	if path == "" {
		path = openai.DefaultCompletionPath
	}
	return OpenAI{
		config: config,
		name:   name,
		path:   path,
		client: openai.NewClient(config.Endpoint.URL, path, config.Endpoint.APIKey),
	}
}

// Call runs one batch completion and maps choice 0 into the raw result shape
// shared by batch and stream parsing.
func (o OpenAI) Call(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResult, error) {
	resp, err := o.client.DoCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.name, err)
	}

	result := &openai.CompletionResult{}
	if len(resp.Choices) > 0 {
		result = &openai.CompletionResult{
			Text:         resp.Choices[0].Text,
			FinishReason: resp.Choices[0].FinishReason,
			Logprobs:     resp.Choices[0].Logprobs,
		}
	}
	logOpenAIResponse(o.name, result)
	return result, nil
}

// SetHTTPTransport swaps the HTTP transport. Used by the eval harness for
// cassette record and replay.
func (o OpenAI) SetHTTPTransport(rt http.RoundTripper) {
	o.client.SetHTTPTransport(rt)
}

func (o OpenAI) LogRequest(req *openai.CompletionRequest, maxLines int) {
	logger.Debug("%s provider request:\n  URL: %s%s\n  Model: %s\n  Temperature: %.2f\n  MaxTokens: %d\n  MaxLines: %d\n  Prompt length: %d chars\n  Prompt:\n%s",
		o.name,
		o.config.Endpoint.URL,
		o.path,
		req.Model,
		req.Temperature,
		req.MaxTokens,
		maxLines,
		len(req.Prompt),
		req.Prompt)
}

// Request fills the config-derived OpenAI fields shared by every dialect.
// Prompt, suffix, and stop tokens remain dialect protocol facts.
func (o OpenAI) Request(prompt string, stop []string) *openai.CompletionRequest {
	return &openai.CompletionRequest{
		Model:       o.config.Endpoint.Model,
		Prompt:      prompt,
		Temperature: o.config.Temperature,
		MaxTokens:   o.config.Endpoint.MaxTokens,
		Logprobs:    logprobCount(o.config),
		Stop:        stop,
	}
}

// logprobCount is the completions `logprobs` request value: one most-likely
// token per position is enough to read the chosen token's confidence.
func logprobCount(config *types.ProviderConfig) int {
	if config.Logprobs {
		return 1
	}
	return 0
}

func logOpenAIResponse(name string, result *openai.CompletionResult) {
	logger.Debug("%s provider response:\n  Text length: %d chars\n  FinishReason: %s\n  StoppedEarly: %v\n  Text:\n%s",
		name,
		len(result.Text),
		result.FinishReason,
		result.StoppedEarly,
		result.Text)
}

// streamSpec is the dialect-selected stream behavior for one built request.
// Sweep uses prefill and first-line validation. FIM attaches cursor-line
// context around raw insertion lines. Zeta-2 uses a cursor-marker line
// transform and its editable-region window. Engine only sees the
// CompletionStream built from it.
type streamSpec struct {
	WindowStart        int
	OldLines           []string
	Prefill            string
	FirstLineValidator func(*RequestState, string) error
	LineTransform      func(string) (string, bool, error)
	// FinalLine emits one last line after the stream ends. Used by
	// transforms that hold back a line until they know it is the last one.
	FinalLine func() (string, bool)
}

// lineStreamSession is the streaming Call runtime. It forwards visible lines
// to engine while Finish parses the raw text collected by the OpenAI client.
type lineStreamSession struct {
	name        string
	stream      *openai.LineStream
	windowStart int
	oldLines    []string
	lines       chan string
	cancelCh    chan struct{}
	cancelOnce  sync.Once

	prefill            string
	firstLineValidator func(*RequestState, string) error
	lineTransform      func(string) (string, bool, error)
	finalLine          func() (string, bool)
	parse              func(*RequestState, *openai.CompletionResult) (*types.CompletionResponse, error)
	state              *RequestState

	validated bool
	err       error
}

func (o OpenAI) startStream(
	ctx context.Context,
	state *RequestState,
	req *openai.CompletionRequest,
	spec streamSpec,
	parse func(*RequestState, *openai.CompletionResult) (*types.CompletionResponse, error),
) (engine.CompletionStream, error) {
	run := &lineStreamSession{
		name:               o.name,
		stream:             o.client.DoLineStream(ctx, req, state.Window.MaxLines),
		windowStart:        spec.WindowStart,
		oldLines:           spec.OldLines,
		lines:              make(chan string, 100),
		cancelCh:           make(chan struct{}),
		prefill:            spec.Prefill,
		firstLineValidator: spec.FirstLineValidator,
		lineTransform:      spec.LineTransform,
		finalLine:          spec.FinalLine,
		parse:              parse,
		state:              state,
	}
	go run.forward()
	return run, nil
}

func (s *lineStreamSession) Lines() <-chan string {
	return s.lines
}

func (s *lineStreamSession) Window() (int, []string) {
	return s.windowStart, s.oldLines
}

func (s *lineStreamSession) Cancel() {
	s.cancelOnce.Do(func() {
		s.stream.Cancel()
		close(s.cancelCh)
	})
}

// Finish turns the accumulated stream text into the same raw result shape as
// batch Call, then invokes the dialect parse function.
func (s *lineStreamSession) Finish() (*types.CompletionResponse, error) {
	rawResult := s.doneResult()
	if s.err != nil {
		return nil, s.err
	}
	if rawResult.Err != nil {
		return nil, rawResult.Err
	}

	result := &openai.CompletionResult{
		Text:         rawResult.Text,
		FinishReason: rawResult.FinishReason,
		StoppedEarly: rawResult.StoppedEarly,
		Logprobs:     rawResult.Logprobs,
	}
	logOpenAIResponse(s.name, result)

	response, err := s.parse(s.state, result)
	if err != nil {
		return nil, err
	}
	attachConfidence(response, result)
	return response, nil
}

func (s *lineStreamSession) forward() {
	defer close(s.lines)

	if s.prefill != "" {
		for line := range strings.SplitSeq(strings.TrimSuffix(s.prefill, "\n"), "\n") {
			if !s.send(line) {
				return
			}
		}
	}

	for rawLine := range s.stream.LinesChan() {
		line := rawLine
		emit := true
		if s.lineTransform != nil {
			var err error
			line, emit, err = s.lineTransform(rawLine)
			if err != nil {
				s.err = err
				s.Cancel()
				return
			}
		}
		if emit && !s.emit(line) {
			return
		}
	}

	if s.finalLine != nil {
		if line, emit := s.finalLine(); emit && !s.emit(line) {
			return
		}
	}
}

func (s *lineStreamSession) emit(line string) bool {
	if s.firstLineValidator != nil && !s.validated {
		if err := s.firstLineValidator(s.state, line); err != nil {
			s.err = err
			s.Cancel()
			return false
		}
		s.validated = true
	}
	return s.send(line)
}

func (s *lineStreamSession) send(line string) bool {
	select {
	case s.lines <- line:
		return true
	case <-s.cancelCh:
		return false
	}
}

func (s *lineStreamSession) doneResult() openai.CompletionResult {
	result, ok := <-s.stream.DoneChan()
	if !ok {
		return openai.CompletionResult{FinishReason: "cancelled", StoppedEarly: true}
	}
	return result
}

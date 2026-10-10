// Package mockglm implements a local stand-in for the OpenCode Go
// chat-completions endpoint.
//
// It exists so the bridge can be driven end to end by a real Codex CLI without
// a live upstream credential: the mock answers with a scripted sequence of tool
// calls and a final message, streaming in deliberately awkward chunk
// boundaries. It also records every request it receives, which is how the
// bridge's own test fixtures are captured.
package mockglm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Turn is one scripted assistant response.
type Turn struct {
	// Kind is "tool" or "text".
	Kind string
	// Text is the assistant text for a "text" turn.
	Text string
	// Reasoning is emitted as reasoning_content deltas before the turn body.
	Reasoning string
	// ToolName is the preferred tool for a "tool" turn. When empty, or when the
	// request does not offer that tool, the first offered tool is used.
	ToolName string
	// Arguments overrides the generated arguments for a "tool" turn.
	Arguments string
	// Command, when set, is the shell command a "tool" turn asks exec_command
	// to run. Codex 0.162.1 exposes command execution as exec_command(cmd), not
	// as shell(command).
	Command string
}

// Options configures the mock.
type Options struct {
	// Model is reported back in responses. Empty means "echo the request's model".
	Model string
	// Turns is the script. Once exhausted the mock answers with a final text
	// message so a client loop always terminates.
	Turns []Turn
	// Split, when > 0, writes the SSE stream in chunks of that many bytes to
	// exercise client-side reassembly.
	Split int
	// NoUsage omits the usage block from the final chunk.
	NoUsage bool
	// CaptureDir, when set, records every received request body there.
	CaptureDir string
	// Status, when >= 400, makes every request fail with that status and Body.
	Status int
	// Body is the error body used with Status.
	Body string
	// PromptTokens is the prompt_tokens reported in usage. Raising it above a
	// model's compaction threshold makes Codex compact, which is how the
	// compaction path is exercised without a million-token transcript.
	PromptTokens int
	// CompletionTokens is the completion_tokens reported in usage.
	CompletionTokens int
}

// Server is an http.Handler implementing the mock.
type Server struct {
	opts Options

	mu       sync.Mutex
	turn     int
	requests []Request
}

// Request is a recorded upstream request.
type Request struct {
	Path string
	Body json.RawMessage
}

// New returns a Server with the given options.
func New(opts Options) *Server {
	if opts.Model == "" {
		opts.Model = "glm-5.3-flash"
	}
	return &Server{opts: opts}
}

// Requests returns a copy of every request body the mock has received.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// TurnCount reports how many scripted turns have been consumed.
func (s *Server) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := readAll(r)
	s.record(r.URL.Path, body)

	if s.opts.Status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.opts.Status)
		msg := s.opts.Body
		if msg == "" {
			msg = "mock upstream error"
		}
		_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":"mock_error"}}`, msg)
		return
	}

	req := parseRequest(body)
	model := s.opts.Model
	if req.Model != "" {
		model = req.Model
	}

	turn := s.nextTurn(req)
	if !req.Stream {
		writeJSON(w, s.completeBody(model, turn))
		return
	}
	s.stream(w, model, turn)
}

func (s *Server) record(path string, body []byte) {
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: path, Body: json.RawMessage(body)})
	n := len(s.requests)
	dir := s.opts.CaptureDir
	s.mu.Unlock()
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.json", n)), body, 0o600)
}

// nextTurn returns the turn to serve for this request, advancing the script.
// The script's last entry is a text turn, which keeps any client loop finite.
func (s *Server) nextTurn(req chatRequest) Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn >= len(s.opts.Turns) {
		return Turn{Kind: "text", Text: "ocgo mock: script exhausted."}
	}
	t := s.opts.Turns[s.turn]
	s.turn++
	if t.Kind == "tool" {
		name := t.ToolName
		if name == "" || !hasTool(req, name) {
			name = firstToolName(req)
		}
		if name == "" {
			return Turn{Kind: "text", Text: "ocgo mock: no tools offered."}
		}
		t.ToolName = name
		if t.Arguments == "" {
			if t.Command != "" && name == "exec_command" {
				b, _ := json.Marshal(map[string]string{"cmd": t.Command})
				t.Arguments = string(b)
			} else {
				t.Arguments = defaultArguments(req, name)
			}
		}
	}
	return t
}

func (s *Server) stream(w http.ResponseWriter, model string, t Turn) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	const id = "chatcmpl-mock"
	emit := func(delta map[string]any, finish any) {
		s.writeSSE(w, flusher, map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
	}

	emit(map[string]any{"role": "assistant"}, nil)
	if t.Reasoning != "" {
		for _, piece := range splitString(t.Reasoning, 24) {
			emit(map[string]any{"reasoning_content": piece}, nil)
		}
	}
	if t.Kind == "tool" {
		emit(map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_mock_1", "type": "function",
			"function": map[string]any{"name": t.ToolName, "arguments": ""},
		}}}, nil)
		// Split the argument JSON mid-token to force client-side reassembly.
		for _, piece := range splitString(t.Arguments, 7) {
			emit(map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "function": map[string]any{"arguments": piece},
			}}}, nil)
		}
		emit(map[string]any{}, "tool_calls")
	} else {
		for _, piece := range splitString(t.Text, 16) {
			emit(map[string]any{"content": piece}, nil)
		}
		emit(map[string]any{}, "stop")
	}

	if !s.opts.NoUsage {
		s.writeSSE(w, flusher, map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": model, "choices": []any{},
			"usage": s.usageMap(),
		})
	}
	s.writeRaw(w, flusher, "data: [DONE]\n\n")
}

func (s *Server) completeBody(model string, t Turn) map[string]any {
	msg := map[string]any{"role": "assistant"}
	if t.Reasoning != "" {
		msg["reasoning_content"] = t.Reasoning
	}
	finish := "stop"
	if t.Kind == "tool" {
		msg["tool_calls"] = []any{map[string]any{
			"id": "call_mock_1", "type": "function",
			"function": map[string]any{"name": t.ToolName, "arguments": t.Arguments},
		}}
		finish = "tool_calls"
	} else {
		msg["content"] = t.Text
	}
	return map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion", "model": model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   s.usageMap(),
	}
}

// usageMap renders the configured usage figures in chat-completions shape.
func (s *Server) usageMap() map[string]any {
	prompt := s.opts.PromptTokens
	if prompt == 0 {
		prompt = 1200
	}
	completion := s.opts.CompletionTokens
	if completion == 0 {
		completion = 45
	}
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      prompt + completion,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 1024,
		},
	}
}

func (s *Server) writeSSE(w http.ResponseWriter, flusher http.Flusher, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeRaw(w, flusher, "data: "+string(b)+"\n\n")
}

func (s *Server) writeRaw(w http.ResponseWriter, flusher http.Flusher, sse string) {
	if s.opts.Split > 0 {
		for _, piece := range splitString(sse, s.opts.Split) {
			_, _ = w.Write([]byte(piece))
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}
	_, _ = w.Write([]byte(sse))
	if flusher != nil {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// --- request inspection -----------------------------------------------------

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []chatTool `json:"tools"`
}

type chatTool struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Function struct {
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func (t chatTool) toolName() string {
	if t.Function.Name != "" {
		return t.Function.Name
	}
	return t.Name
}

func parseRequest(body []byte) chatRequest {
	var req chatRequest
	_ = json.Unmarshal(body, &req)
	return req
}

func hasTool(req chatRequest, name string) bool {
	for _, t := range req.Tools {
		if t.toolName() == name {
			return true
		}
	}
	return false
}

func firstToolName(req chatRequest) string {
	for _, t := range req.Tools {
		if n := t.toolName(); n != "" {
			return n
		}
	}
	return ""
}

// defaultArguments builds arguments that satisfy the named tool's schema, using
// the schema the client itself sent so the generated call is always well-formed.
func defaultArguments(req chatRequest, name string) string {
	// apply_patch is a freeform grammar tool: its arguments are a patch, not JSON.
	if name == "apply_patch" {
		return "*** Begin Patch\n*** Add File: ocgo_mock.txt\n+created by the ocgo mock upstream\n*** End Patch"
	}
	var params struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	for _, t := range req.Tools {
		if t.toolName() == name {
			_ = json.Unmarshal(t.Function.Parameters, &params)
		}
	}
	keys := params.Required
	if len(keys) == 0 {
		for k := range params.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	args := map[string]any{}
	for _, k := range keys {
		if k == "command" {
			if params.Properties[k].Type == "string" {
				args[k] = "printf 'ocgo-mock\\n'"
			} else {
				args[k] = []any{"bash", "-lc", "printf 'ocgo-mock\\n'"}
			}
			continue
		}
		switch params.Properties[k].Type {
		case "array":
			args[k] = []any{}
		case "object":
			args[k] = map[string]any{}
		case "number", "integer":
			args[k] = 0
		case "boolean":
			args[k] = false
		default:
			args[k] = ""
		}
	}
	b, _ := json.Marshal(args)
	return string(b)
}

// splitString cuts s into pieces of at most n bytes.
func splitString(s string, n int) []string {
	if n <= 0 || len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func readAll(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return []byte(sb.String())
}

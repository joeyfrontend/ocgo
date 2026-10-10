// Command mockglm runs the scripted chat-completions stand-in used to drive the
// bridge end to end without a live upstream credential.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"ocgo/internal/mockglm"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:3457", "listen address")
	capture := flag.String("capture", "", "directory to record received request bodies")
	split := flag.Int("split", 0, "write the SSE stream in chunks of N bytes")
	noUsage := flag.Bool("no-usage", false, "omit the usage block from the final chunk")
	status := flag.Int("status", 0, "fail every request with this HTTP status")
	body := flag.String("body", "", "error body used with -status")
	reasoning := flag.Bool("reasoning", true, "emit reasoning_content deltas")
	turnsFile := flag.String("turns-file", "", "JSON array of scripted turns; overrides -script")
	script := flag.String("script", "", "comma-separated turn kinds: tool|text (default is a 4-turn script)")
	tool := flag.String("tool", "exec_command", "preferred tool name for tool turns")
	promptTokens := flag.Int("prompt-tokens", 0, "prompt_tokens reported in usage (default 1200)")
	flag.Parse()

	opts := mockglm.Options{
		Split:            *split,
		NoUsage:          *noUsage,
		Status:           *status,
		Body:             *body,
		CaptureDir:       *capture,
		PromptTokens:     *promptTokens,
		CompletionTokens: 45,
	}

	switch {
	case *turnsFile != "":
		b, err := os.ReadFile(*turnsFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mockglm:", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(b, &opts.Turns); err != nil {
			fmt.Fprintln(os.Stderr, "mockglm: bad -turns-file:", err)
			os.Exit(1)
		}
	case *script != "":
		for i, kind := range splitKinds(*script) {
			turn := mockglm.Turn{Kind: kind}
			if kind == "tool" {
				turn.ToolName = *tool
				if i > 0 {
					turn.ToolName = "exec_command"
				}
			} else {
				turn.Text = "ocgo mock: task complete."
			}
			opts.Turns = append(opts.Turns, turn)
		}
	default:
		opts.Turns = defaultTurns()
	}
	if *reasoning {
		for i := range opts.Turns {
			if opts.Turns[i].Reasoning == "" {
				opts.Turns[i].Reasoning = "Reasoning about the next step."
			}
		}
	}

	srv := mockglm.New(opts)
	mux := http.NewServeMux()
	mux.Handle("/v1/chat/completions", srv)
	mux.Handle("/v1/chat/completions/", srv)
	mux.Handle("/zen/go/v1/chat/completions", srv)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	log.Printf("mockglm listening on http://%s (%d scripted turns)", *addr, len(opts.Turns))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, "mockglm:", err)
		os.Exit(1)
	}
}

// defaultTurns drives a real Codex session through read, inspect and edit using
// the tool names Codex 0.162.1 actually offers.
func defaultTurns() []mockglm.Turn {
	return []mockglm.Turn{
		{Kind: "tool", ToolName: "exec_command", Command: "cat notes.txt"},
		{Kind: "tool", ToolName: "exec_command", Command: "ls -la && sed -n '1,20p' calc.py"},
		{Kind: "tool", ToolName: "exec_command", Command: "python3 - <<'PY'\nimport pathlib\np = pathlib.Path('calc.py')\np.write_text(p.read_text().replace('a - b', 'a + b'))\nPY\ncat calc.py"},
		{Kind: "text", Text: "Fixed the subtraction bug in calc.py: `add` now returns `a + b`."},
	}
}

func splitKinds(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		out = []string{"text"}
	}
	return out
}

// Package main provides a reference implementation of an llm-bridge client.
//
// # How it works
//
// The llm-bridge client-server protocol is line-oriented JSON over stdio:
//
//  1. The client spawns the bridge binary as a subprocess (e.g. `./build/llm-bridge`).
//  2. The bridge emits a `{"event": "ready"}` JSON line on stdout once initialized.
//     The client MUST consume/wait for this `ready` event before sending any command.
//  3. Commands are sent as JSON lines to the bridge's stdin:
//     - Prompt: `{"method": "prompt", "params": {"text": "..."}}`
//     - Tool result: `{"method": "tool_result", "params": {"id": "...", "result": ...}}`
//     - Cancel: `{"method": "cancel"}`
//     - Quit: `{"method": "quit"}`
//  4. Events stream back from the bridge's stdout as JSON lines:
//     - `ready`, `thinking`, `chunk`, `tool_call`, `turn_end`, `files_changed`,
//       `hook_action`, `error`, `cancelled`.
//
// This reference CLI handles all these events, implements local file/shell tools,
// displays thinking blocks, and supports interactive tool approval.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"llm-bridge/llm"
	"llm-bridge/protocol"
)

// ============================================================================
// 1. Local Tool Implementations
// ============================================================================
// When the LLM requests a tool call (e.g. read, write, shell, grep, glob,
// search_replace), the client executes it locally on disk and sends the result
// back to the bridge via the `tool_result` command.

func executeToolCall(tc llm.ToolCall) (string, error) {
	switch tc.Name {
	case "read":
		var args struct {
			Path   string `json:"path"`
			Offset *int   `json:"offset"`
			Limit  *int   `json:"limit"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		data, err := os.ReadFile(args.Path)
		if err != nil {
			return "", err
		}
		// If offset/limit are provided, slice lines accordingly
		if args.Offset != nil || args.Limit != nil {
			lines := strings.Split(string(data), "\n")
			start := 0
			if args.Offset != nil && *args.Offset > 0 {
				start = *args.Offset
				if start > len(lines) {
					start = len(lines)
				}
			}
			end := len(lines)
			if args.Limit != nil && *args.Limit >= 0 {
				end = start + *args.Limit
				if end > len(lines) {
					end = len(lines)
				}
			}
			lines = lines[start:end]
			return strings.Join(lines, "\n"), nil
		}
		return string(data), nil

	case "write":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(args.Path), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(args.Path, []byte(args.Content), 0644); err != nil {
			return "", err
		}
		return "ok", nil

	case "shell":
		var args struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		cmd := exec.Command("bash", "-c", args.Command)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out) + "\n" + err.Error(), err
		}
		return string(out), nil

	case "grep":
		var args struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		if args.Pattern == "" {
			return "", fmt.Errorf("missing pattern")
		}
		searchPath := args.Path
		if searchPath == "" {
			searchPath = "."
		}
		var matches []string
		_ = filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				// skip common heavy dirs
				if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "build" {
					return filepath.SkipDir
				}
				return nil
			}
			data, rErr := os.ReadFile(path)
			if rErr != nil {
				return nil
			}
			if strings.Contains(string(data), args.Pattern) {
				matches = append(matches, path)
			}
			return nil
		})
		return strings.Join(matches, "\n"), nil

	case "glob":
		var args struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		pattern := args.Pattern
		if args.Path != "" {
			pattern = filepath.Join(args.Path, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", err
		}
		return strings.Join(matches, "\n"), nil

	case "search_replace":
		var args struct {
			Path    string `json:"path"`
			Search  string `json:"search"`
			Replace string `json:"replace"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			return "", err
		}
		if args.Path == "" || args.Search == "" {
			return "", fmt.Errorf("missing path or search string")
		}
		data, err := os.ReadFile(args.Path)
		if err != nil {
			return "", err
		}
		content := string(data)
		if !strings.Contains(content, args.Search) {
			return "", fmt.Errorf("string %q not found in %s", args.Search, args.Path)
		}
		newContent := strings.Replace(content, args.Search, args.Replace, 1)
		if err := os.WriteFile(args.Path, []byte(newContent), 0644); err != nil {
			return "", err
		}
		return "ok", nil

	default:
		return "", fmt.Errorf("unknown tool %q", tc.Name)
	}
}

// isReadOnlyTool reports whether a tool only reads data (never mutates the filesystem).
func isReadOnlyTool(name string) bool {
	switch name {
	case "read", "grep", "glob":
		return true
	default:
		return false
	}
}

func askConfirmation(reader *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}

// ============================================================================
// 2. Protocol Helpers (Commands & Tool Results)
// ============================================================================

func sendPrompt(w io.Writer, text string) error {
	params, _ := json.Marshal(protocol.PromptParams{Text: text})
	return json.NewEncoder(w).Encode(protocol.Command{
		Method: protocol.MethodPrompt,
		Params: params,
	})
}

func sendToolResult(w io.Writer, id, result, status string) error {
	params, _ := json.Marshal(protocol.ToolResultParams{
		ID:     id,
		Result: json.RawMessage(result),
		Status: status,
	})
	return json.NewEncoder(w).Encode(protocol.Command{
		Method: protocol.MethodToolResult,
		Params: params,
	})
}

// ============================================================================
// 3. UI / Rendering Helpers (Thinking & Terminal Colors)
// ============================================================================

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type thinkingRenderer struct {
	enabled bool
	color   bool
	buf     strings.Builder
}

func newThinkingRenderer(enabled bool) *thinkingRenderer {
	return &thinkingRenderer{
		enabled: enabled,
		color:   os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout),
	}
}

func (r *thinkingRenderer) add(fragment string) {
	if r.enabled {
		r.buf.WriteString(fragment)
	}
}

func (r *thinkingRenderer) flush() {
	if !r.enabled || r.buf.Len() == 0 {
		return
	}
	text := strings.TrimSpace(r.buf.String())
	r.buf.Reset()
	if text == "" {
		return
	}
	if r.color {
		fmt.Println("\x1b[90m\x1b[3m┌─ reasoning ──────────────────────────────")
		for _, ln := range strings.Split(text, "\n") {
			fmt.Printf("│ %s\n", ln)
		}
		fmt.Println("└──────────────────────────────────────────\x1b[0m")
	} else {
		fmt.Printf("\n[reasoning]\n%s\n[/reasoning]\n", text)
	}
}

// ============================================================================
// 4. Main Client Loop
// ============================================================================

func main() {
	bridgeFlag := flag.String("bridge", "", "path to llm-bridge binary (default: ./build/llm-bridge or LLM_BRIDGE_PATH)")
	showThinking := flag.Bool("show-thinking", true, "display model chain-of-thought as a block before the answer")
	providerFlag := flag.String("provider", "", "default LLM provider override (e.g. deepseek, google)")
	modelFlag := flag.String("model", "", "default model override")
	flag.Parse()

	// Locate bridge binary
	bridgePath := *bridgeFlag
	if bridgePath == "" {
		bridgePath = os.Getenv("LLM_BRIDGE_PATH")
	}
	if bridgePath == "" {
		bridgePath = "./build/llm-bridge"
	}
	if _, err := os.Stat(bridgePath); err != nil {
		// Fallback to searching PATH
		if path, err := exec.LookPath("llm-bridge"); err == nil {
			bridgePath = path
		} else {
			fmt.Fprintf(os.Stderr, "error: bridge binary not found at %q (run 'make build' first?)\n", bridgePath)
			os.Exit(1)
		}
	}

	// Prepare arguments for the bridge subprocess.
	// -debug (or -logfile) is required so logs go to disk/stderr instead of polluting stdout.
	args := []string{"-debug"}
	if *providerFlag != "" {
		args = append(args, "-provider", *providerFlag)
	}
	if *modelFlag != "" {
		args = append(args, "-model", *modelFlag)
	}

	cmd := exec.Command(bridgePath, args...)
	cmd.Stderr = os.Stderr // Pipe bridge logs to our stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get bridge stdin: %v\n", err)
		os.Exit(1)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get bridge stdout: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting bridge subprocess: %s %s\n", bridgePath, strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start bridge: %v\n", err)
		os.Exit(1)
	}

	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	// Support large tool results up to 8MB per JSON line
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	// ------------------------------------------------------------------------
	// STEP 1: Wait for the "ready" event from the bridge
	// ------------------------------------------------------------------------
	fmt.Println("Waiting for bridge to be ready...")
	readyReceived := false
	for scanner.Scan() {
		line := scanner.Text()
		var ev struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.Event == string(protocol.EventReady) {
			readyReceived = true
			break
		}
	}
	if !readyReceived {
		fmt.Fprintln(os.Stderr, "error: bridge exited before emitting ready event")
		return
	}
	fmt.Println("Bridge is ready! Type your prompt below (or '#hook' to run a hook, 'quit' to exit).")
	if *showThinking {
		fmt.Println("Thinking display: ON (use -show-thinking=false to hide)")
	}
	fmt.Println(strings.Repeat("─", 60))

	reader := bufio.NewReader(os.Stdin)
	thinking := newThinkingRenderer(*showThinking)

	// ------------------------------------------------------------------------
	// STEP 2: Interactive REPL loop
	// ------------------------------------------------------------------------
	for {
		fmt.Print("\n>>> ")
		text, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Println()
				break
			}
			fmt.Fprintf(os.Stderr, "read error: %v\n", err)
			break
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if text == "quit" || text == "exit" {
			// Ask bridge to quit gracefully
			_ = json.NewEncoder(stdin).Encode(protocol.Command{Method: protocol.MethodQuit})
			break
		}

		// Send prompt (if text starts with '#', the bridge automatically treats it as a hook!)
		if err := sendPrompt(stdin, text); err != nil {
			fmt.Printf("[error sending prompt] %v\n", err)
			break
		}

		// --------------------------------------------------------------------
		// STEP 3: Consume response events until turn_end, error, or hook_action
		// --------------------------------------------------------------------
		for scanner.Scan() {
			line := scanner.Text()

			// Generic event parser struct covering all outbound event fields
			var ev struct {
				Event        string          `json:"event"`
				Text         string          `json:"text,omitempty"`
				ID           string          `json:"id,omitempty"`
				Name         string          `json:"name,omitempty"`
				Input        json.RawMessage `json:"input,omitempty"`
				StopReason   string          `json:"stop_reason,omitempty"`
				Message      string          `json:"message,omitempty"`
				InputTokens  int             `json:"input_tokens,omitempty"`
				OutputTokens int             `json:"output_tokens,omitempty"`
				TotalTokens  int             `json:"total_tokens,omitempty"`
				Files        []string        `json:"files,omitempty"`
				Output       string          `json:"output,omitempty"`
				Error        string          `json:"error,omitempty"`
			}

			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				fmt.Printf("[json parse error] %v (line: %s)\n", err, line)
				continue
			}

			switch ev.Event {
			case string(protocol.EventThinking):
				// Chain-of-thought streaming fragment
				thinking.add(ev.Text)

			case string(protocol.EventChunk):
				// Answer text streaming fragment
				thinking.flush()
				fmt.Print(ev.Text)

			case string(protocol.EventToolCall):
				thinking.flush()
				fmt.Printf("\n\n⚙️ [Tool Call Requested]\n  ID:    %s\n  Name:  %s\n  Input: %s\n", ev.ID, ev.Name, string(ev.Input))

				// Auto-approve read-only tools; prompt user for mutating tools
				approved := isReadOnlyTool(ev.Name)
				if approved {
					fmt.Println("  (auto-approved read-only tool)")
				} else {
					approved = askConfirmation(reader, "  Execute this tool? (y/N) ")
				}

				if approved {
					result, err := executeToolCall(llm.ToolCall{
						ID:        ev.ID,
						Name:      ev.Name,
						Arguments: string(ev.Input),
					})
					if err != nil {
						fmt.Printf("  ❌ Tool execution failed: %v\n", err)
						resultJSON, _ := json.Marshal(map[string]any{"error": err.Error()})
						_ = sendToolResult(stdin, ev.ID, string(resultJSON), "error")
					} else {
						fmt.Println("  ✅ Tool executed successfully.")
						resultJSON, _ := json.Marshal(map[string]any{"result": result})
						_ = sendToolResult(stdin, ev.ID, string(resultJSON), "success")
					}
				} else {
					fmt.Println("  🚫 Tool call denied by user.")
					resultJSON, _ := json.Marshal(map[string]any{"error": "user denied tool call"})
					_ = sendToolResult(stdin, ev.ID, string(resultJSON), "error")
				}

			case string(protocol.EventFilesChanged):
				if len(ev.Files) > 0 {
					fmt.Printf("\n📂 [Files Modified]:\n")
					for _, f := range ev.Files {
						fmt.Printf("   - %s\n", f)
					}
				}

			case string(protocol.EventHookAction):
				// Hooks return output or error
				if ev.Error != "" {
					fmt.Printf("\n❌ [Hook %q Error]: %s\n", ev.Name, ev.Error)
				} else {
					fmt.Printf("\n🔧 [Hook %q Output]:\n%s\n", ev.Name, ev.Output)
				}
				goto nextPrompt

			case string(protocol.EventTurnEnd):
				thinking.flush()
				fmt.Printf("\n\n📊 [Turn Finished]\n  Stop Reason: %s\n  Tokens:      input=%d, output=%d, total=%d\n",
					ev.StopReason, ev.InputTokens, ev.OutputTokens, ev.TotalTokens)
				goto nextPrompt

			case string(protocol.EventError):
				fmt.Printf("\n❌ [Bridge Error]: %s\n", ev.Message)
				goto nextPrompt

			case string(protocol.EventCancelled):
				fmt.Println("\n⚠️ [Turn Cancelled]")
				goto nextPrompt

			case string(protocol.EventReady):
				// Ignore extra ready events if any

			default:
				// Ignore unrecognized events to remain forward-compatible
			}
		}
	nextPrompt:
	}
	fmt.Println("Goodbye!")
}

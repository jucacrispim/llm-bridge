// Copyright 2026 Juca Crispim <juca@poraodojuca.dev>
//
// This file is part of llm-bridge.
//
// llm-bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// llm-bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with llm-bridge. If not, see <http://www.gnu.org/licenses/>.

package server

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appcontext "llm-bridge/context"
	"llm-bridge/history"
	"llm-bridge/hooks"
	"llm-bridge/knowledge"
	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/protocol"
	"llm-bridge/sandbox"
	"llm-bridge/tools"
)

//go:embed system_prompt.md
var systemPromptFS embed.FS

var defaultSystemPrompt = func() string {
	b, err := systemPromptFS.ReadFile("system_prompt.md")
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded system_prompt.md: %v", err))
	}
	return strings.TrimSpace(string(b))
}()

// Sandboxes groups the sandboxes the server uses: Tools for the external
// processes (shell, grep, hooks), Curl for the curl tool, which alone has
// network access and runs under its own minimal policy. A nil Tools means "run
// unsandboxed", and a nil Curl also keeps curl out of the tool list.
type Sandboxes struct {
	Tools *sandbox.Sandbox
	Curl  *sandbox.Sandbox
	// ToolBin is the llm-tools binary that runs the filesystem tools under
	// Tools. Empty keeps them in-process, which is the case with the sandbox
	// off: a process boundary without a policy buys nothing.
	ToolBin   string
	CurlHosts []string
}

type state struct {
	cwd            string
	knowledgeBases []json.RawMessage
	// kbEmbed is the embedder for the knowledge base(s); nil means disabled. It
	// may be a LazyEmbedder, which loads the ONNX model on first use.
	kbEmbed knowledge.Embedder
	// kbBaseDir is where the bases live (kbBaseDir/<key>/data.json). Empty
	// means disabled.
	kbBaseDir string
	// kbScope is the configured scope, set from -kb-scope.
	kbScope string
	// kbScopeOverride is the per-prompt kb_scope override, which persists
	// between prompts like the model, provider and thinking overrides. Empty
	// means "use kbScope".
	kbScopeOverride string
	// kb resolves `knowledge` calls locally: one or two scoped managers, global
	// and/or the current project. nil means disabled.
	kb *knowledge.Set
	// sandboxes holds the policies built at startup. The cwd is not part of
	// them: it changes via set_cwd and is passed per command.
	sandboxes Sandboxes
	// provider is the single fixed provider used when no registry is set
	// (kept for the runWithProvider/Run entry points and existing tests).
	provider llm.LLMProvider
	// providers is an optional registry of named providers that can be
	// switched per request via the prompt's `provider` field. When non-empty it
	// takes precedence over `provider`.
	providers map[string]llm.LLMProvider
	// providerName is the currently active provider in the registry.
	providerName string

	// pendingApprovals holds the mutating tool calls of the current turn still
	// waiting on the client. Each approval runs its tool at once, and the turn
	// resumes when the map is empty. Read-only tools never enter this map.
	pendingApprovals map[string]llm.ToolCall
	history          []llm.Message
	inToolCycle      bool
	firstTurn        bool

	modelOverride          string
	thinkingOverride       *bool
	reasoningLevelOverride *llm.ReasoningLevel
	thinkingBudgetOverride int
	systemOverride         string
	systemPrompt           string

	// aggressivePrune collapses each completed tool-calling turn into the user
	// prompt plus the final answer (-aggressive-prune).
	aggressivePrune bool

	// prune collapses reasoning and tool results into <State>/<Replace>
	// snapshots, keeping the file states and dropping the rest (-prune).
	prune bool

	// fetched holds the tail of what the curl tool returned in this session, so
	// a later mutating call can be checked for copied content. Only the turn
	// loop touches it, so it needs no lock.
	fetched string

	cancel context.CancelFunc
	// cancelled reports whether the current turn was cancelled. Some clients
	// queue a tool_result right before their own cancel (when the user denies a
	// tool) and the reader goroutine may handle the cancel first, so a
	// tool_result for a cancelled turn is ignored instead of erroring.
	cancelled bool

	historyLenBeforeTurn int

	totalTokens           int
	totalPromptTokens     int
	totalCompletionTokens int

	turnPromptTokens     int
	turnCompletionTokens int
	// lastPromptTokens is the prompt_tokens of the last provider call in the
	// turn (assigned, not summed): the size of the context sent in it.
	lastPromptTokens int
	// turnCacheHitTokens/turnCacheMissTokens sum the provider's cache accounting
	// over the turn, and go out in turn_end.
	turnCacheHitTokens  int
	turnCacheMissTokens int
	// turnFilesChanged holds the paths written in the turn, reported as a
	// files_changed event at its end. Reset at the start of each prompt.
	turnFilesChanged []string

	// cancelMut guards the `cancel` field, which the reader goroutine writes
	// while runToolCycle reads it.
	cancelMut sync.Mutex
	// writeMut serializes every write to stdout, so the reader goroutine's
	// `cancelled` event cannot interleave with streaming chunks.
	writeMut sync.Mutex
	// historyMut guards the `history` slice, touched by the reader goroutine
	// (cancelTurn) and by runToolCycle. Go through the locked helpers below.
	historyMut sync.Mutex
}

// activeProvider returns the provider for the current request: the registry
// entry named providerName, or the single fixed provider.
func (st *state) activeProvider() llm.LLMProvider {
	if len(st.providers) > 0 {
		if p, ok := st.providers[st.providerName]; ok {
			return p
		}
		for _, p := range st.providers {
			return p
		}
	}
	return st.provider
}

// effectiveSystem returns the system prompt of the request: override, server
// prompt, then the embedded default.
func (st *state) effectiveSystem() string {
	if st.systemOverride != "" {
		return st.systemOverride
	}
	if st.systemPrompt != "" {
		return st.systemPrompt
	}
	return defaultSystemPrompt
}

// appendUser appends a user message to the shared conversation history.
func (st *state) appendUser(content string) {
	st.appendUserWithImages(content, nil)
}

// appendUserWithImages appends a user message with optional images to the
// shared history.
func (st *state) appendUserWithImages(content string, images []llm.Image) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendUserWithImages(&st.history, content, images)
}

// appendAssistant appends an assistant message to the shared history, keeping
// any partial thinking/content collected from the stream.
func (st *state) appendAssistant(resp *llm.ChatResponse) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendAssistant(&st.history, resp)
}

// appendToolResult appends a tool response to the shared conversation history.
func (st *state) appendToolResult(id, content string) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendToolResult(&st.history, id, content)
}

// resolveKnowledge executes a knowledge tool call locally and appends the
// result, so the model always gets one response per tool_call even when the KB
// is disabled.
func (st *state) resolveKnowledge(id, args string) {
	if st.kb == nil {
		st.appendToolResult(id, "knowledge base disabled")
		return
	}
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	out, err := st.kb.Execute(raw)
	if err != nil {
		out = "error: " + err.Error()
	}
	st.appendToolResult(id, out)
}

// availableTools returns the tools offered to the model. curl is left out when
// its sandbox is nil, because nobody could run it.
func (st *state) availableTools() []llm.Tool {
	all := tools.All()
	if st.sandboxes.Curl != nil {
		return all
	}
	out := make([]llm.Tool, 0, len(all))
	for _, tool := range all {
		if tool.Name == "curl" {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// fetchedTailLimit bounds what the taint check keeps: a page body fits, and a
// long session does not grow without bound.
const fetchedTailLimit = 64 << 10

// rememberFetched keeps the tail of what curl returned, so a later mutating
// call can be checked against it. The envelope stays: a match on the body is
// what matters.
func (st *state) rememberFetched(out string) {
	st.fetched = out
	if len(st.fetched) > fetchedTailLimit {
		st.fetched = st.fetched[len(st.fetched)-fetchedTailLimit:]
	}
}

// confirmMeta builds the extra detail a `tool_confirm` carries: the commands a
// shell one-liner really runs, a coarse risk label and whether the arguments
// copy content fetched earlier in the session.
func (st *state) confirmMeta(tc llm.ToolCall) protocol.ConfirmMeta {
	meta := protocol.ConfirmMeta{Tainted: tools.Tainted(tools.Args(tc.Arguments), st.fetched)}
	switch tc.Name {
	case "shell":
		command := tools.CommandOf(tc.Arguments)
		meta.Segments = tools.Segments(command)
		meta.Risk = tools.Risk(command, meta.Segments)
	case "write", "search_replace":
		// A write into .llm-bridge edits the context and the hooks the next
		// session runs, so it carries the same weight as a shell one-liner
		// that names the directory.
		meta.Risk = tools.PathRisk(st.cwd, filePathFromArgs(tc.Arguments))
	}
	return meta
}

// executeTool runs a single tool call and appends its result. The context can
// be cancelled, which interrupts a running shell/grep process; a result is not
// appended for a cancelled turn, because the assistant message was already
// sanitized away and the result would be an orphan.
func (st *state) executeTool(tc llm.ToolCall) {
	if tc.Name == "knowledge" {
		st.resolveKnowledge(tc.ID, tc.Arguments)
		return
	}
	exec := tools.NewExecutorWithToolbox(st.cwd, st.sandboxes.Tools, st.sandboxes.Curl, st.sandboxes.CurlHosts, st.sandboxes.ToolBin)
	ctx, cancel := context.WithCancel(context.Background())
	st.setCancel(cancel)
	out, err := exec.Execute(ctx, tc.Name, tc.Arguments)
	st.setCancel(nil)
	cancel()
	if tc.Name == "curl" {
		// Audit line: the fetch is the only thing the bridge does that leaves
		// the machine, and its result is what a later injection feeds on. This
		// goes to the log file only (-logfile), never to the protocol stream.
		logger.Infof("curl url=%s bytes=%d ok=%t", curlURL(tc.Arguments), len(out), err == nil)
	}
	if err != nil {
		out = "error: " + err.Error()
	}
	if st.isCancelled() {
		return
	}
	if tc.Name == "curl" {
		// Keep what came back, so a later call that quotes the page can be
		// flagged as tainted. Recorded before the result is appended: the
		// taint check runs when the *next* confirmation is built.
		st.rememberFetched(out)
	}
	st.appendToolResult(tc.ID, out)
}

// curlURL extracts the url argument of a curl call, for the audit log.
func curlURL(rawArgs string) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal([]byte(rawArgs), &a)
	return a.URL
}

// kbBaseMetadata is the part of a knowledge base's client-reported metadata
// (set_knowledge_bases) that links it to a project: name and path.
type kbBaseMetadata struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// projectKBName returns the display name of the knowledge base linked to the
// current project, or "" when nothing matches. A base is linked when its path is
// the cwd, an ancestor or a descendant of it, or when its last path element is
// the project name.
func (st *state) projectKBName() string {
	if st.cwd == "" {
		return ""
	}
	project := filepath.Base(st.cwd)
	cwd := strings.TrimRight(st.cwd, string(filepath.Separator))
	for _, raw := range st.knowledgeBases {
		var b kbBaseMetadata
		if err := json.Unmarshal(raw, &b); err != nil {
			continue // ignore malformed metadata entries
		}
		if b.Path == "" {
			continue
		}
		p := strings.TrimRight(b.Path, string(filepath.Separator))
		if p == cwd ||
			strings.HasPrefix(p, cwd+string(filepath.Separator)) ||
			strings.HasPrefix(cwd, p+string(filepath.Separator)) {
			return b.Name
		}
		if filepath.Base(b.Path) == project {
			return b.Name
		}
	}
	return ""
}

// effectiveKBScope returns the scope of the turn: the per-prompt override when
// set, otherwise -kb-scope. Unknown values fall back to global.
func (st *state) effectiveKBScope() string {
	scope := st.kbScopeOverride
	if scope == "" {
		scope = st.kbScope
	}
	if !knowledge.IsScope(scope) {
		return knowledge.ScopeGlobal
	}
	return scope
}

// kbManager loads the manager stored under key, or returns an empty one when
// data.json is corrupt, so the `knowledge` tool still resolves locally.
func (st *state) kbManager(key string) *knowledge.Manager {
	m, err := knowledge.Load(st.kbBaseDir, key, st.kbEmbed)
	if err != nil {
		logger.Warningf("knowledge: load %q: %v", key, err)
		return knowledge.NewManager(st.kbEmbed, key)
	}
	return m
}

// loadKB (re)builds st.kb from the effective scope and the cwd. It runs on
// set_cwd and when a prompt changes the scope, so a cwd change in the global
// scope leaves the shared KB untouched.
func (st *state) loadKB() {
	if st.kbEmbed == nil || st.kbBaseDir == "" {
		st.kb = nil
		return
	}
	global := func() knowledge.Scope {
		m := st.kbManager(knowledge.GlobalKey)
		// The shared KB has no per-project seed subdirectory: it is seeded
		// from the global seed files only.
		m.SetSeedProject("")
		return knowledge.Scope{Name: knowledge.ScopeGlobal, M: m}
	}
	project := ""
	if st.cwd != "" {
		project = filepath.Base(st.cwd)
	}

	switch st.effectiveKBScope() {
	case knowledge.ScopeProject:
		if project == "" {
			return // no cwd yet: keep whatever is loaded
		}
		st.kb = knowledge.NewSet(knowledge.ScopeProject,
			knowledge.Scope{Name: knowledge.ScopeProject, M: st.kbManager(project)})
	case knowledge.ScopeBoth:
		scopes := []knowledge.Scope{global()}
		def := knowledge.ScopeGlobal
		if project != "" {
			scopes = append(scopes,
				knowledge.Scope{Name: knowledge.ScopeProject, M: st.kbManager(project)})
		}
		st.kb = knowledge.NewSet(def, scopes...)
	default: // knowledge.ScopeGlobal
		st.kb = knowledge.NewSet(knowledge.ScopeGlobal, global())
	}
}

// knowledgeNotice returns the single line that tells the model which knowledge
// base(s) exist, or "" when the KB is disabled. It names the scopes only, never
// their contents, so the prefix stays cache-friendly.
func (st *state) knowledgeNotice() string {
	if st.kb == nil {
		return ""
	}
	names := st.kb.Scopes()
	if len(names) == 1 && names[0] == knowledge.ScopeGlobal {
		return "Knowledge base available (tool: knowledge), shared across projects. " +
			"Search it before answering questions about architecture, past decisions or conventions."
	}
	kbName := st.projectKBName()
	if kbName == "" && st.cwd != "" {
		kbName = filepath.Base(st.cwd)
	}
	if len(names) == 1 {
		line := "Knowledge base"
		if kbName != "" {
			line += " for project \"" + kbName + "\""
		}
		return line + " available (tool: knowledge). Search it before answering " +
			"questions about this project's architecture or past decisions."
	}
	line := "Knowledge bases available (tool: knowledge): \"global\" (shared across projects)"
	if kbName != "" {
		line += " and the project KB for \"" + kbName + "\""
	}
	return line + ". Search them before answering questions about architecture or past " +
		"decisions; add takes a \"scope\" field (\"global\" or \"project\"), defaulting to \"global\"."
}

// sanitizeHistory strips ephemeral blocks and orphaned tool_calls messages.
func (st *state) sanitizeHistory() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.Sanitize(&st.history)
}

// historySnapshot returns a copy of the history, safe to hand to a provider
// while the reader goroutine sanitizes it.
func (st *state) historySnapshot() []llm.Message {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	out := make([]llm.Message, len(st.history))
	copy(out, st.history)
	return out
}

// historyLen returns the current length of the shared conversation history.
func (st *state) historyLen() int {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	return len(st.history)
}

// hasPendingApproval reports whether the turn waits on an approval for id.
func (st *state) hasPendingApproval(id string) bool {
	_, ok := st.pendingApprovals[id]
	return ok
}

// recordFileChanged adds path to the turn's files_changed list, without
// duplicates. Only write and search_replace call it.
func (st *state) recordFileChanged(path string) {
	if path == "" {
		return
	}
	for _, p := range st.turnFilesChanged {
		if p == path {
			return
		}
	}
	st.turnFilesChanged = append(st.turnFilesChanged, path)
}

// parseHook splits a hook message into the hook name and its arguments:
// "#git status" gives "git" and ["status"], "#algo" gives "algo" and none.
func parseHook(text string) (string, []string) {
	rest := strings.TrimSpace(strings.TrimPrefix(text, "#"))
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// filePathFromArgs returns the "path" argument of a tool call, or "".
func filePathFromArgs(args string) string {
	if args == "" {
		return ""
	}
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return ""
	}
	return a.Path
}

// resolvePromptImages turns the prompt's image attachments into llm.Images: a
// path is read from disk (relative ones against cwd), data is decoded as base64
// and a url goes through to the provider. A missing or unreadable image is an
// error naming it.
func resolvePromptImages(cwd string, params []protocol.ImageParam) ([]llm.Image, error) {
	if len(params) == 0 {
		return nil, nil
	}
	images := make([]llm.Image, 0, len(params))
	for _, p := range params {
		path := p.Path
		if path != "" && !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		im, err := llm.ResolveImage(path, p.URL, p.Data, p.MIME)
		if err != nil {
			return nil, fmt.Errorf("image: %w", err)
		}
		logger.Tracef("image path=%q url=%q mime=%s b64_len=%d",
			path, p.URL, im.MIME, len(im.Data))
		images = append(images, im)
	}
	return images, nil
}

// cacheCounts splits the prompt tokens of one call into the cached part and the
// rest. A provider that reports no cache leaves cached at zero.
func cacheCounts(u *llm.Usage) (cached, uncached int) {
	cached = u.CachedTokens
	uncached = u.PromptTokens - cached
	if uncached < 0 {
		uncached = 0
	}
	return cached, uncached
}

// parseReasoningEffort turns a prompt's reasoning_effort into the two hints the
// providers understand: a level on the common scale, or a token budget. An
// empty or unrecognized value clears both, so the provider keeps its own
// default.
func parseReasoningEffort(raw string) (*llm.ReasoningLevel, int) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, 0
	}
	if llm.ValidReasoningLevel(s) {
		level := llm.ReasoningLevel(s)
		return &level, 0
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return nil, n
	}
	return nil, 0
}

// collapseTurn compresses a completed tool-calling turn into the user prompt
// that opened it plus the final answer, dropping the tool calls, the results and
// the reasoning. The prefix stays byte-identical, so the provider's prefix cache
// survives.
func (st *state) collapseTurn() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()

	start := st.historyLenBeforeTurn
	if start < 0 || start >= len(st.history) {
		return
	}
	last := st.history[len(st.history)-1]
	// No final plain assistant answer → nothing to collapse cleanly.
	if last.Role != llm.RoleAssistant || len(last.ToolCalls) > 0 {
		return
	}
	// A turn with no intermediate tool activity is already prompt + answer.
	if len(st.history)-start <= 2 {
		return
	}

	collapsed := make([]llm.Message, 0, start+2)
	collapsed = append(collapsed, st.history[:start]...)
	collapsed = append(collapsed, st.history[start]) // the user prompt of this turn
	collapsed = append(collapsed, llm.Message{
		Role:    llm.RoleAssistant,
		Content: last.Content,
	})
	st.history = collapsed
}

type fileEvent struct {
	path    string
	kind    string // "state" or "replace"
	content string
	seq     int
}

// pruneTurn collapses reasoning and tool calls into <State> and <Replace> user
// snapshots, so the file states survive and the rest of the turn does not. See
// -prune in docs/source/usage.rst.
func (st *state) pruneTurn() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()

	start := st.historyLenBeforeTurn
	if start < 0 || start >= len(st.history) {
		return
	}
	last := st.history[len(st.history)-1]
	// A valid turn to prune must end with a plain assistant response (no pending tool calls).
	if last.Role != llm.RoleAssistant || len(last.ToolCalls) > 0 {
		return
	}
	// Turns with no intermediate tool activity do not need file pruning.
	if len(st.history)-start <= 2 {
		return
	}

	turnMessages := st.history[start : len(st.history)-1]

	type toolCallInfo struct {
		name string
		args map[string]any
	}
	// Map tool call IDs to their metadata (tool name and parsed arguments).
	calls := make(map[string]toolCallInfo)

	for _, msg := range turnMessages {
		if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				if tc.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
				}
				calls[tc.ID] = toolCallInfo{name: tc.Name, args: args}
			}
		}
	}

	var events []fileEvent
	seqCounter := 0

	// Extract file events (reads, writes, replaces) from tool results and tool arguments.
	for _, msg := range turnMessages {
		if msg.Role == "tool" {
			info, ok := calls[msg.ToolCallID]
			if !ok {
				continue
			}

			switch info.name {
			case "read":
				path, _ := info.args["path"].(string)
				if path != "" {
					events = append(events, fileEvent{
						path:    path,
						kind:    "state",
						content: msg.Content,
						seq:     seqCounter,
					})
					seqCounter++
				}
			case "write":
				path, _ := info.args["path"].(string)
				content, _ := info.args["content"].(string)
				if path != "" {
					events = append(events, fileEvent{
						path:    path,
						kind:    "state",
						content: content,
						seq:     seqCounter,
					})
					seqCounter++
				}
			case "search_replace":
				path, _ := info.args["path"].(string)
				search, _ := info.args["search"].(string)
				replace, _ := info.args["replace"].(string)
				if path != "" {
					repContent := fmt.Sprintf("search:\n%s\n---\nreplace:\n%s", search, replace)
					events = append(events, fileEvent{
						path:    path,
						kind:    "replace",
						content: repContent,
						seq:     seqCounter,
					})
					seqCounter++
				}
			}
		} else if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				info := calls[tc.ID]
				if info.name == "write" {
					path, _ := info.args["path"].(string)
					content, _ := info.args["content"].(string)
					if path != "" {
						events = append(events, fileEvent{
							path:    path,
							kind:    "state",
							content: content,
							seq:     seqCounter,
						})
						seqCounter++
					}
				}
			}
		}
	}

	eventsByPath := make(map[string][]fileEvent)
	for _, ev := range events {
		eventsByPath[ev.path] = append(eventsByPath[ev.path], ev)
	}

	// Apply path-based reduction rule:
	// Baseline = the last state (read/write) snapshot for each path.
	// Keep the baseline plus all replaces occurring after it; drop prior events.
	var survivingEvents []fileEvent
	for _, pathEvents := range eventsByPath {
		lastStateIdx := -1
		for i := len(pathEvents) - 1; i >= 0; i-- {
			if pathEvents[i].kind == "state" {
				lastStateIdx = i
				break
			}
		}

		if lastStateIdx != -1 {
			survivingEvents = append(survivingEvents, pathEvents[lastStateIdx:]...)
		} else {
			survivingEvents = append(survivingEvents, pathEvents...)
		}
	}

	// Restore original chronological order among surviving events across paths.
	sort.Slice(survivingEvents, func(i, j int) bool {
		return survivingEvents[i].seq < survivingEvents[j].seq
	})

	// Rebuild the history for this turn: original user prompt + file snapshots/deltas + final answer.
	var newHistory []llm.Message
	newHistory = append(newHistory, st.history[:start]...)
	newHistory = append(newHistory, st.history[start])

	datetime := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	for _, ev := range survivingEvents {
		var markup string
		if ev.kind == "state" {
			markup = fmt.Sprintf("<State %s %s>%s</State>", ev.path, datetime, ev.content)
		} else {
			markup = fmt.Sprintf("<Replace %s %s>%s</Replace>", ev.path, datetime, ev.content)
		}
		newHistory = append(newHistory, llm.Message{
			Role:    llm.RoleUser,
			Content: markup,
		})
	}

	newHistory = append(newHistory, llm.Message{
		Role:    llm.RoleAssistant,
		Content: last.Content,
	})

	st.history = newHistory
}

// write serializes a write to w, so a `cancelled` event from the reader
// goroutine cannot interleave with streaming chunks.
func (st *state) write(w io.Writer, s string) error {
	st.writeMut.Lock()
	defer st.writeMut.Unlock()
	_, err := fmt.Fprint(w, s)
	return err
}

// setCancel stores the active Chat cancel func (nil when no Chat is running).
func (st *state) setCancel(c context.CancelFunc) {
	st.cancelMut.Lock()
	st.cancel = c
	st.cancelMut.Unlock()
}

// cancelActive invokes and clears the active Chat cancel func, if any.
func (st *state) cancelActive() {
	st.cancelMut.Lock()
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.cancelMut.Unlock()
}

// setCancelled records whether the turn was cancelled. Guarded by cancelMut:
// the reader goroutine writes it while the main loop may read it.
func (st *state) setCancelled(v bool) {
	st.cancelMut.Lock()
	st.cancelled = v
	st.cancelMut.Unlock()
}

// isCancelled reports whether the current turn was cancelled.
func (st *state) isCancelled() bool {
	st.cancelMut.Lock()
	defer st.cancelMut.Unlock()
	return st.cancelled
}

// cancelTurn interrupts the in-flight turn and resets its state, without
// emitting any event. It keeps the turn's history: the prompt and any partial
// content stay in the conversation.
func (st *state) cancelTurn() {
	st.cancelActive()
	st.setCancelled(true)
	st.inToolCycle = false
	st.pendingApprovals = nil
	// Drop the assistant tool_calls messages that never got their tool results:
	// an assistant tool_calls message must be followed by one tool response per
	// call, and the provider rejects the request with HTTP 400 otherwise.
	st.sanitizeHistory()
}

type setCwdParams struct {
	Cwd string `json:"cwd"`
}

type setKnowledgeBasesParams struct {
	Bases []json.RawMessage `json:"bases"`
}

type toolConfirmParams struct {
	ID string `json:"id"`
}

func handleLine(line string, st *state, w io.Writer) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}

	var cmd protocol.InboundCommand
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		return string(protocol.NewError("invalid JSON: " + err.Error())), false
	}

	switch cmd.Method {
	case string(protocol.MethodPrompt):
		if st.activeProvider() == nil {
			return string(protocol.NewError("no LLM provider configured")), false
		}
		var p protocol.PromptParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid prompt params: " + err.Error())), false
		}

		// A message starting with "#" runs a local hook script instead of the
		// LLM: the rest of the message goes to the script as arguments, in the
		// project's cwd, and the output comes back as one hook_action event.
		if text := strings.TrimSpace(p.Text); strings.HasPrefix(text, "#") {
			// In its own goroutine, so a slow script does not block the command
			// loop. Hooks are not cancelable: once launched, a script runs to
			// completion.
			hookName, hookArgs := parseHook(text)
			go func() {
				out, err := hooks.RunContext(context.Background(), st.cwd, hookName, hookArgs, st.sandboxes.Tools)
				if err != nil {
					_ = st.write(w, string(protocol.NewHook(hookName, "", err.Error()))+"\n")
					return
				}
				_ = st.write(w, string(protocol.NewHook(hookName, out, ""))+"\n")
			}()
			return "", false
		}

		if st.inToolCycle {
			return string(protocol.NewError("cannot send new prompt while awaiting tool results")), false
		}
		// A new prompt starts a fresh turn: reset the cancelled flag so a
		// stale tool_result from a previous cancelled turn is not swallowed.
		st.setCancelled(false)
		if st.firstTurn {
			entries, _ := appcontext.Load(st.cwd)
			for _, e := range entries {
				st.appendUser(e.Render())
			}
			// When the knowledge base is enabled, surface a single minimal line
			// about the available KB(s) (not their contents) so the model knows
			// the `knowledge` tool is available and is nudged to consult it.
			if line := st.knowledgeNotice(); line != "" {
				st.appendUser(line)
			}
			st.firstTurn = false
		}
		if p.Provider != "" {
			if len(st.providers) == 0 {
				return string(protocol.NewError("provider switching not available: " + p.Provider)), false
			}
			if _, ok := st.providers[p.Provider]; !ok {
				return string(protocol.NewError("unknown provider: " + p.Provider)), false
			}
			st.providerName = p.Provider
		}
		if p.Model != "" {
			st.modelOverride = p.Model
		}
		if p.Thinking != nil {
			st.thinkingOverride = p.Thinking
		}
		if p.ReasoningEffort != nil {
			level, budget := parseReasoningEffort(*p.ReasoningEffort)
			st.reasoningLevelOverride = level
			st.thinkingBudgetOverride = budget
		}
		if p.KBScope != "" {
			if !knowledge.IsScope(p.KBScope) {
				return string(protocol.NewError("unknown kb_scope: " + p.KBScope +
					" (use global, project or both)")), false
			}
			if p.KBScope != st.effectiveKBScope() {
				st.kbScopeOverride = p.KBScope
				// Reload so the new scope's KB(s) are the ones in use.
				st.loadKB()
			}
		}
		sys := p.System
		if sys == "" {
			sys = p.SystemPrompt
		}
		if sys != "" {
			st.systemOverride = sys
		}
		st.turnPromptTokens = 0
		st.turnCompletionTokens = 0
		st.lastPromptTokens = 0
		st.turnCacheHitTokens = 0
		st.turnCacheMissTokens = 0
		st.turnFilesChanged = nil
		st.historyLenBeforeTurn = st.historyLen() // context preserved on cancel
		images, err := resolvePromptImages(st.cwd, p.Images)
		if err != nil {
			return string(protocol.NewError(err.Error())), false
		}
		st.appendUserWithImages(p.Text, images)
		return runToolCycle(st, w)

	case string(protocol.MethodCancel):
		st.cancelTurn()
		return string(protocol.NewCancelled()), false

	case string(protocol.MethodSetCwd):
		var p setCwdParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid set_cwd params: " + err.Error())), false
		}
		st.cwd = p.Cwd
		// Rebuild the knowledge base to follow the new cwd (in the global scope
		// the shared KB is unchanged; in the project/both scopes the project's
		// data.json is loaded from disk).
		st.loadKB()
		return "", false

	case string(protocol.MethodSetKnowledgeBases):
		var p setKnowledgeBasesParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid set_knowledge_bases params: " + err.Error())), false
		}
		st.knowledgeBases = p.Bases
		return "", false

	case string(protocol.MethodToolResult):
		// Ignored for backwards compatibility: the bridge runs every tool
		// itself now, and an older client still sends this.
		return "", false

	case string(protocol.MethodToolConfirm):
		// The client approved a mutating call: run it, append the result and
		// resume the turn when nothing is left pending.
		var p toolConfirmParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid tool_confirm params: " + err.Error())), false
		}
		if !st.inToolCycle || len(st.pendingApprovals) == 0 {
			// A late approval for a cancelled turn, ignored like a late
			// tool_result.
			return "", false
		}
		tc, ok := st.pendingApprovals[p.ID]
		// Only an id this turn is waiting on counts: a stale approval racing
		// into a newer turn must be ignored.
		if !ok {
			return "", false
		}
		delete(st.pendingApprovals, p.ID)
		st.executeTool(tc)
		if st.isCancelled() {
			return "", false
		}
		if len(st.pendingApprovals) == 0 {
			st.inToolCycle = false
			return runToolCycle(st, w)
		}
		return "", false

	case string(protocol.MethodQuit):
		return "", true

	default:
		return string(protocol.NewError("unknown method " + cmd.Method)), false
	}
}

func runToolCycle(st *state, w io.Writer) (string, bool) {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		// Publish the cancel func so the reader goroutine can interrupt this
		// Chat when a `cancel` arrives mid-stream.
		st.setCancel(cancel)
		emittedChunk := false
		emittedReasoning := false
		// Keep the streamed text here too: a turn cancelled mid-stream still
		// gets its partial output persisted below.
		var partialContent strings.Builder
		var partialReasoning strings.Builder
		// An assistant tool_calls message without its tool responses makes the
		// provider reject the request with HTTP 400. Sanitize right before the
		// snapshot, so what is sent is always valid.
		st.sanitizeHistory()
		resp, err := st.activeProvider().Chat(ctx, llm.ChatRequest{
			Messages:       st.historySnapshot(),
			Tools:          st.availableTools(),
			System:         st.effectiveSystem(),
			Model:          st.modelOverride,
			Thinking:       st.thinkingOverride,
			ReasoningLevel: st.reasoningLevelOverride,
			ThinkingBudget: st.thinkingBudgetOverride,
			// The reasoning streams before the content.
			OnReasoning: func(s string) {
				partialReasoning.WriteString(s)
				emittedReasoning = true
				_ = st.write(w, string(protocol.NewThinking(s))+"\n")
			},
		}, func(s string) {
			partialContent.WriteString(s)
			emittedChunk = true
			_ = st.write(w, string(protocol.NewChunk(s))+"\n")
		})
		st.setCancel(nil)
		if err != nil {
			cancel()
			if errors.Is(err, context.Canceled) {
				// Cancelled: cancelTurn keeps the prompt, so persist whatever the
				// model produced before the interrupt and keep the context.
				if partialContent.Len() > 0 || partialReasoning.Len() > 0 {
					st.appendAssistant(&llm.ChatResponse{
						Content:   partialContent.String(),
						Reasoning: partialReasoning.String(),
					})
				}
				return "", false
			}
			logger.Errorf("provider error: %v", err)
			return string(protocol.NewError(err.Error())), false
		}
		// Cancelled while Chat was finishing: committing the response as-is
		// would leave an assistant tool_calls message that never gets its
		// results.
		// Persist only the produced content (never the tool_calls) and
		// re-sanitize so nothing half-finished lingers in the history.
		if st.isCancelled() {
			cancel()
			if resp != nil {
				content := resp.Content
				if content == "" {
					content = partialContent.String()
				}
				reasoning := resp.Reasoning
				if reasoning == "" {
					reasoning = partialReasoning.String()
				}
				if content != "" || reasoning != "" {
					st.appendAssistant(&llm.ChatResponse{Content: content, Reasoning: reasoning})
				}
			}
			st.sanitizeHistory()
			return "", false
		}
		cancel()

		inputTokens := 0
		outputTokens := 0
		if resp.Usage != nil {
			inputTokens = resp.Usage.PromptTokens
			outputTokens = resp.Usage.CompletionTokens
		}
		st.turnPromptTokens += inputTokens
		st.turnCompletionTokens += outputTokens
		if resp.Usage != nil {
			hit, miss := cacheCounts(resp.Usage)
			st.turnCacheHitTokens += hit
			st.turnCacheMissTokens += miss
			st.lastPromptTokens = resp.Usage.PromptTokens
		}
		totalTokensThisCall := inputTokens + outputTokens
		st.totalTokens += totalTokensThisCall
		st.totalPromptTokens += inputTokens
		st.totalCompletionTokens += outputTokens

		// Cache accounting from the provider's usage, as one trace line that
		// only reaches -logfile/-debug.
		if resp.Usage != nil {
			hit, miss := cacheCounts(resp.Usage)
			hitPct := 0.0
			if hit+miss > 0 {
				hitPct = float64(hit) * 100 / float64(hit+miss)
			}
			model := resp.Model
			if model == "" {
				model = st.activeProvider().Model()
			}
			logger.Tracef("usage provider=%s model=%s prompt=%d hit=%d miss=%d out=%d hit_pct=%.1f",
				st.activeProvider().Name(), model, inputTokens, hit, miss, outputTokens, hitPct)
		}

		if !emittedChunk && resp.Content != "" {
			_ = st.write(w, string(protocol.NewChunk(resp.Content))+"\n")
		}
		// Providers that return the reasoning without streaming it still get the
		// thinking surfaced to the client, as a single event.
		if !emittedReasoning && resp.Reasoning != "" {
			_ = st.write(w, string(protocol.NewThinking(resp.Reasoning))+"\n")
		}

		st.appendAssistant(resp)

		// Read-only calls run here and now and go out as a `tool_call` display
		// event; mutating ones go out as `tool_confirm` and only run on
		// approval. The split is per call, because a knowledge write must never
		// run without a human and a lookup must never wait for one.
		st.pendingApprovals = make(map[string]llm.ToolCall)
		for _, tc := range resp.ToolCalls {
			if st.isCancelled() {
				return "", false
			}
			var input any = map[string]any{}
			if tc.Arguments != "" {
				input = json.RawMessage(tc.Arguments)
			}
			if !tools.MutatesCall(tc.Name, tc.Arguments) {
				evt, err := protocol.NewToolCall(tc.ID, tc.Name, input)
				if err == nil {
					_ = st.write(w, string(evt)+"\n")
				}
				// Execute the read-only tool now and append its real result so
				// the turn keeps moving without a client round-trip.
				st.executeTool(tc)
				continue
			}
			// One keystroke approves a whole one-liner, so past the segment
			// limit there is no approval to offer. The refusal is a tool result,
			// not an error, so the model can split the command and retry.
			if tc.Name == "shell" {
				command := tools.CommandOf(tc.Arguments)
				if segments := tools.Segments(command); len(segments) > tools.MaxShellSegments {
					st.appendToolResult(tc.ID, fmt.Sprintf(
						"error: too many segments (%d > %d): this command runs %d separate commands at once. "+
							"Split it into separate calls, one action per call, so each one can be reviewed.",
						len(segments), tools.MaxShellSegments, len(segments)))
					continue
				}
			}
			evt, err := protocol.NewToolConfirmWithMeta(tc.ID, tc.Name, input, st.confirmMeta(tc))
			if err == nil {
				_ = st.write(w, string(evt)+"\n")
			}
			// Track the files the tool is about to modify, for files_changed.
			switch tc.Name {
			case "write", "search_replace":
				st.recordFileChanged(filePathFromArgs(tc.Arguments))
			}
			st.pendingApprovals[tc.ID] = tc
		}

		if len(resp.ToolCalls) > 0 {
			usageEvt := protocol.NewUsageDelta(inputTokens, outputTokens, totalTokensThisCall)
			_ = st.write(w, string(usageEvt)+"\n")
		}

		if len(resp.ToolCalls) == 0 {
			model := resp.Model
			if model == "" {
				model = st.activeProvider().Model()
			}
			// Context usage: the last prompt of the turn over the model's static
			// window. An unknown model reports a null context_pct.
			contextTokens := st.lastPromptTokens
			contextWindow := 0
			var contextPct *float64
			if win, ok := llm.ContextWindow(model); ok && win > 0 {
				contextWindow = win
				pct := float64(contextTokens) / float64(win)
				contextPct = &pct
			}
			logger.Tracef("context model=%s used=%d window=%d", model, contextTokens, contextWindow)
			turnTotalTokens := st.turnPromptTokens + st.turnCompletionTokens
			newEnd := protocol.NewTurnEnd(resp.StopReason, contextPct,
				model, st.turnPromptTokens, st.turnCompletionTokens, turnTotalTokens,
				st.turnCacheHitTokens, st.turnCacheMissTokens,
				contextTokens, contextWindow)
			_ = st.write(w, string(newEnd)+"\n")
			// Report the modified files, sorted for deterministic output.
			if len(st.turnFilesChanged) > 0 {
				files := make([]string, len(st.turnFilesChanged))
				copy(files, st.turnFilesChanged)
				sort.Strings(files)
				_ = st.write(w, string(protocol.NewFilesChanged(files))+"\n")
			}

			newUsage := protocol.NewUsageDelta(st.turnPromptTokens, st.turnCompletionTokens, turnTotalTokens)
			_ = st.write(w, string(newUsage)+"\n")
			// Before historyLenBeforeTurn is reset, because both need the turn's
			// start marker.
			if st.aggressivePrune {
				st.collapseTurn()
			}
			if st.prune {
				st.pruneTurn()
			}
			st.sanitizeHistory()
			st.inToolCycle = false
			st.pendingApprovals = nil
			st.historyLenBeforeTurn = 0
			return "", false
		}

		if len(st.pendingApprovals) > 0 {
			st.inToolCycle = true
			return "", false
		}
		// Every call was read-only or knowledge, already executed: call Chat
		// again with the results in the history.
		continue
	}
}

// inboundLine carries either a raw command line or the reader's terminal error.
type inboundLine struct {
	line string
	err  error
}

// providerNames returns the sorted keys of the provider registry for logging.
func providerNames(providers map[string]llm.LLMProvider) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// run starts the server loop with either a single fixed provider or a named
// registry. When providers is non-empty it wins over the single provider.
func run(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string, systemPrompt string, aggressivePrune bool, prune bool, kbBaseDir string, kbScope string, kbEmbed knowledge.Embedder, sbs Sandboxes) error {

	if len(providers) > 0 {
		if defaultName == "" {
			for name := range providers {
				defaultName = name
				break
			}
		}
		logger.Infof("starting bridge with providers %v (default %s)", providerNames(providers), defaultName)
	}
	if aggressivePrune {
		logger.Infof("aggressive prune enabled: collapsing tool-calling turns")
	}
	if kbEmbed != nil {
		logger.Infof("knowledge base enabled (scope: %s, base dir: %s)", kbScope, kbBaseDir)
	}
	if prune {
		logger.Infof("prune enabled: collapsing reasoning and preserving file snapshots")
	}
	if sbs.Tools != nil {
		logger.Infof("tool sandbox enabled (caged: %s)", sbs.Tools.Bin)
	}
	if sbs.Curl != nil {
		hosts := "any"
		if len(sbs.CurlHosts) > 0 {
			hosts = strings.Join(sbs.CurlHosts, ",")
		}
		logger.Infof("curl sandbox enabled (caged: %s, hosts: %s)", sbs.Curl.Bin, hosts)
	}

	st := &state{providers: providers, providerName: defaultName,
		systemPrompt: systemPrompt, firstTurn: true,
		aggressivePrune: aggressivePrune, prune: prune,
		kbBaseDir: kbBaseDir, kbScope: kbScope, kbEmbed: kbEmbed, sandboxes: sbs}

	// Load the KB up front: with the global scope it does not depend on the
	// cwd, and the client may never send set_cwd.
	st.loadKB()

	_ = st.write(w, string(protocol.NewReady())+"\n")
	logger.Debugf("ready sent")

	// The reader goroutine keeps consuming stdin while the main loop is blocked
	// inside a streaming Chat. `cancel` is handled here, so it interrupts the
	// request at once; everything else goes to the main loop.
	cmdCh := make(chan inboundLine, 16)
	go func() {
		scanner := bufio.NewScanner(r)
		// Tool results can be large (a whole file read, a grep with many
		// matches) and arrive as one JSON line. Past the scanner's 64KB default
		// it returns ErrTooLong and the bridge exits, so raise the limit.
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // up to 8MB per line
		for scanner.Scan() {
			line := scanner.Text()
			logger.Debugf("received: %s", line)
			var cmd protocol.InboundCommand
			if err := json.Unmarshal([]byte(line), &cmd); err == nil &&
				cmd.Method == string(protocol.MethodCancel) {
				// Cancel in flight: stop the streaming request and reply at once.
				st.cancelTurn()
				_ = st.write(w, string(protocol.NewCancelled())+"\n")
				continue
			}
			cmdCh <- inboundLine{line: line}
		}
		if err := scanner.Err(); err != nil {
			cmdCh <- inboundLine{err: err}
		}
		close(cmdCh)
	}()

	for item := range cmdCh {
		if item.err != nil {
			logger.Errorf("scanner error: %v", item.err)
			return item.err
		}
		resp, quit := handleLine(item.line, st, w)
		if quit {
			logger.Infof("quit requested")
			return nil
		}
		if resp != "" {
			// notest
			logger.Debugf("sending response: %s", resp)
			if err := st.write(w, resp+"\n"); err != nil {
				logger.Errorf("error writing response: %v", err)
				return err
			}
		}
	}
	logger.Infof("bridge finished")
	return nil
}

// runWithProvider starts the server loop with a single fixed provider.
func runWithProvider(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	if provider != nil {
		providers := map[string]llm.LLMProvider{provider.Name(): provider}
		return run(r, w, providers, provider.Name(), "", false, false, "", "", nil, Sandboxes{})
	}
	return run(r, w, nil, "", "", false, false, "", "", nil, Sandboxes{})
}

// Run starts the server loop reading from r and writing to w.
func Run(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	// notest
	return runWithProvider(r, w, provider)
}

// RunWithProviders starts the server loop with a registry of named providers
// that can be switched per request via the prompt's `provider` field. defaultName
// selects the initial active provider.
func RunWithProviders(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string) error {
	// notest
	return RunWithSystemPrompt(r, w, providers, defaultName, "")
}

// RunWithSystemPrompt starts the server loop with a registry of named providers
// and an initial global system prompt.
func RunWithSystemPrompt(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string, systemPrompt string) error {
	// notest
	return run(r, w, providers, defaultName, systemPrompt, false, false, "", "", nil, Sandboxes{})

}

// RunWithOptions starts the server loop with a registry of named providers, an
// initial global system prompt, and the aggressive-prune option (see state).
func RunWithOptions(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName, systemPrompt string, aggressivePrune bool, prune bool) error {
	// notest
	return run(r, w, providers, defaultName, systemPrompt, aggressivePrune, prune, "", "", nil, Sandboxes{})
}

// RunWithKnowledgeBase starts the server loop with the knowledge base wired:
// kbBaseDir holds the bases (kbBaseDir/<key>/data.json), kbScope selects which
// ones are used and kbEmbed loads them. A nil kbEmbed or an empty kbBaseDir
// keeps the KB disabled. This is the entry point used by cmd/llm-bridge.
func RunWithKnowledgeBase(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName, systemPrompt string, aggressivePrune bool, prune bool, kbBaseDir string, kbScope string, kbEmbed knowledge.Embedder, sbs Sandboxes) error {
	// notest
	return run(r, w, providers, defaultName, systemPrompt, aggressivePrune, prune, kbBaseDir, kbScope, kbEmbed, sbs)
}

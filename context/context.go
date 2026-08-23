// Package context loads the conversation context from Markdown files.
//
// The context is composed of two sources:
//   - General context: ~/.llm-bridge/*.md (applies to all conversations)
//   - Local context:   <cwd>/.llm-bridge/*.md (applies to the current project/directory)
//
// The general context comes before the local one. Within each directory, files
// are sorted alphabetically. Each file becomes a persistent (non-ephemeral)
// user message, preserving its original path.
package context

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"llm-bridge/llm"
	"llm-bridge/logger"
)

const (
	entryBegin = "--- CONTEXT ENTRY BEGIN ---"
	entryEnd   = "--- CONTEXT ENTRY END ---"
)

// Entry represents a single loaded context file.
type Entry struct {
	Path    string // full path of the file
	Content string // content read from the file
}

// Render wraps the entry in the format expected by the conversation:
//
//	--- CONTEXT ENTRY BEGIN ---
//	[<path>]
//	<content>
//	--- CONTEXT ENTRY END ---
func (e Entry) Render() string {
	return entryBegin + "\n[" + e.Path + "]\n" + e.Content + "\n" + entryEnd
}

// Load reads the general (~/.llm-bridge) and local (<cwd>/.llm-bridge) context,
// returning general before local, each directory in alphabetical order.
// Missing directories are ignored without error.
func Load(cwd string) ([]Entry, error) {
	entries := []Entry{}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// notest
		logger.Warningf("context: cannot determine home dir, skipping general context: %v", err)
	} else {
		general, err := readDir(filepath.Join(home, ".llm-bridge"))
		if err != nil {
			// notest
			logger.Warningf("context: error reading general dir: %v", err)
		} else {
			entries = append(entries, general...)
		}
	}

	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			// notest
			logger.Warningf("context: cannot determine cwd, skipping local context: %v", err)
			return entries, nil
		}
	}

	local, err := readDir(filepath.Join(cwd, ".llm-bridge"))
	if err != nil {
		// notest
		logger.Warningf("context: error reading local dir: %v", err)
		return entries, nil
	}
	entries = append(entries, local...)

	return entries, nil
}

// readDir lists the *.md files in dir in alphabetical order, reading the
// contents of each one. If the directory does not exist, it returns empty
// without error. Files that fail to be read are skipped with a log.
func readDir(dir string) ([]Entry, error) {
	entries := []Entry{}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Debugf("context: dir %s does not exist, skipping", dir)
			return entries, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		logger.Debugf("context: %s is not a directory, skipping", dir)
		return entries, nil
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	for _, path := range matches {
		content, err := os.ReadFile(path)
		if err != nil {
			// notest
			logger.Warningf("context: cannot read %s: %v", path, err)
			continue
		}
		entries = append(entries, Entry{
			Path:    path,
			Content: strings.TrimSpace(string(content)),
		})
	}

	return entries, nil
}

// BuildMessages converts the entries into persistent user messages,
// preserving the order.
func BuildMessages(entries []Entry) []llm.Message {
	msgs := make([]llm.Message, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleUser,
			Content: e.Render(),
		})
	}
	return msgs
}

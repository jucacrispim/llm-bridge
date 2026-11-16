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

// Package context loads the conversation context from Markdown files:
// ~/.llm-bridge/*.md first (general), then <cwd>/.llm-bridge/*.md (the
// project). Within a directory the files come alphabetically.
//
// Internals are in docs/source/hacking/context-internals.rst.
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

// Entry is a single loaded context file.
type Entry struct {
	Path    string // full path of the file
	Content string // content read from the file
}

// Render wraps the entry in the markers the conversation expects:
//
//	--- CONTEXT ENTRY BEGIN ---
//	[<path>]
//	<content>
//	--- CONTEXT ENTRY END ---
func (e Entry) Render() string {
	return entryBegin + "\n[" + e.Path + "]\n" + e.Content + "\n" + entryEnd
}

// Load reads the general (~/.llm-bridge) and local (<cwd>/.llm-bridge) context,
// general first, each directory in alphabetical order. A missing directory is
// not an error.
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

// readDir lists the *.md files in dir alphabetically and reads each one. A
// missing directory gives an empty result; a file that fails to read is skipped
// with a log.
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

// BuildMessages converts the entries into persistent user messages, in order.
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

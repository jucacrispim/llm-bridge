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

// Package hooks resolves and runs the local hook scripts a message starting
// with "#" triggers: "#algo foo bar" runs algo.sh with the arguments "foo bar".
//
// See docs/source/hooks.rst for the lookup order and the event.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"llm-bridge/sandbox"
)

// validName matches the hook names allowed to become part of a path. The
// restriction to letters, digits, underscore and hyphen blocks "../x" and "a/b".
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Lookup returns the path of the script for name, looking in the project's
// hooks dir first and then in the general one. An invalid name is an error.
func Lookup(cwd, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("hook: empty name")
	}
	if !validName.MatchString(name) {
		return "", fmt.Errorf("hook: invalid name %q", name)
	}
	dirs := []string{}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".llm-bridge", "hooks"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".llm-bridge", "hooks"))
	}
	for _, dir := range dirs {
		p := filepath.Join(dir, name+".sh")
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("hook: not found: %s", name)
}

// Run executes the hook for name in the project's working directory and returns
// its combined output.
func Run(cwd, name string, args []string) (string, error) {
	return RunContext(context.Background(), cwd, name, args, nil)
}

// RunContext is Run with a context and an optional sandbox. The context cancels
// the script; a non-nil sandbox runs it under caged, and a *sandbox.SetupError
// comes back when the sandbox did not apply, instead of the script's own exit
// status.
func RunContext(ctx context.Context, cwd, name string, args []string, sb *sandbox.Sandbox) (string, error) {
	path, err := Lookup(cwd, name)
	if err != nil {
		return "", err
	}
	cmdArgs := append([]string{path}, args...)
	// The script runs in the project's cwd. A missing or non-directory cwd
	// leaves the process's own working directory.
	dir := ""
	if cwd != "" {
		if info, err := os.Stat(cwd); err == nil && info.IsDir() {
			dir = cwd
		}
	}
	if sb != nil {
		out, err := sb.Run(ctx, dir, "bash", cmdArgs...)
		var se *sandbox.SetupError
		if errors.As(err, &se) {
			return string(out), se
		}
		return string(out), err
	}
	cmd := exec.CommandContext(ctx, "bash", cmdArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

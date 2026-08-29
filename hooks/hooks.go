// Package hooks resolves and executes local hook scripts triggered by the
// bridge when a message starts with a "#" (e.g. "#algo foo bar" runs the
// script .llm-bridge/hooks/algo.sh with arguments "foo bar").
//
// Scripts live in a "hooks" directory under the .llm-bridge dir, mirroring the
// context loading: first the project directory (<cwd>/.llm-bridge/hooks/),
// then the general directory (~/.llm-bridge/hooks/). The script runs in the
// project's working directory and its combined stdout+stderr is returned to
// the client as a hook_action event.
package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

// validName matches hook names allowed to become part of a filesystem path.
// Restricting to letters/digits/underscore/hyphen prevents path traversal
// (e.g. "../x", "a/b", "a b").
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Lookup returns the path of the hook script for the given name, searching
// first the project directory (<cwd>/.llm-bridge/hooks/<name>.sh) and then
// the general directory (~/.llm-bridge/hooks/<name>.sh). Returns an error when
// the name is invalid (empty or containing path separators) or when no script
// is found.
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

// Run executes the hook script for name with the given args in the project's
// working directory and returns its combined stdout+stderr. It returns an
// error when the script cannot be located or when the script exits non-zero.
func Run(cwd, name string, args []string) (string, error) {
	path, err := Lookup(cwd, name)
	if err != nil {
		return "", err
	}
	cmdArgs := append([]string{path}, args...)
	cmd := exec.Command("bash", cmdArgs...)
	// The script runs in the project's cwd so it sees the same working
	// directory as the conversation. If cwd is unset/absent, the process's
	// own working directory is used.
	if cwd != "" {
		if info, err := os.Stat(cwd); err == nil && info.IsDir() {
			cmd.Dir = cwd
		}
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

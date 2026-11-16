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

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exit codes caged uses for its own failures. The command shares the status
// space, so these only shape the error message (see docs/source/sandbox.rst).
const (
	// ExitUsage means caged rejected the policy argv.
	ExitUsage = 2
	// ExitSetupFailed means caged could not apply the sandbox or exec the
	// command.
	ExitSetupFailed = 125
)

// ReportFD is the descriptor caged reports its own failures on: the first one
// above stderr, marked close-on-exec. caged writes nothing there when the
// setup succeeded.
const ReportFD = 3

// waitDelay bounds the wait for the output pipes after the process group was
// killed, so a grandchild holding the pipe does not block the bridge.
const waitDelay = 5 * time.Second

// disableWarnings silences caged's "this kernel does not support feature X"
// warnings. caged prints them on every run and a tool's output goes back to
// the model verbatim, so they would burn tokens on every shell/grep call.
const disableWarnings = "CAGED_DISABLE_WARNINGS=1"

// Sandbox builds exec.Cmd values that run commands under a caged policy.
type Sandbox struct {
	// Bin is the path of the caged binary.
	Bin string
	// Policy is the sandbox built for every command.
	Policy Policy
}

// New returns a sandbox for bin and p. An empty bin or an invalid policy is an
// error, so a bad configuration stops at startup instead of at the first tool
// call.
func New(bin string, p Policy) (*Sandbox, error) {
	if bin == "" {
		return nil, errors.New("sandbox: empty caged binary path")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Sandbox{Bin: bin, Policy: p}, nil
}

// Command returns a command that runs name with args under the policy:
//
//	caged <policy flags> --report-fd 3 -- name args...
//
// The command runs in its own process group, so cancelling the context kills
// whatever it spawned. Use Run unless you need the raw *exec.Cmd.
func (s *Sandbox) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	flags := s.Policy.Args()
	argv := make([]string, 0, len(flags)+4+len(args))
	argv = append(argv, flags...)
	argv = append(argv, "--report-fd", strconv.Itoa(ReportFD))
	argv = append(argv, "--", name)
	argv = append(argv, args...)

	cmd := exec.CommandContext(ctx, s.Bin, argv...)
	// Env of the caged process, not of the command: caged filters it to its
	// base set before exec.
	cmd.Env = cagedEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid = the process group led by the child, i.e. caged and
		// everything it exec'd or forked.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && (errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM)) {
			// Already gone (or not ours): the call to Wait settles it.
			return nil
		}
		return err
	}
	cmd.WaitDelay = waitDelay
	return cmd
}

// Run executes name with args under the policy, in dir (empty means the bridge's
// own working directory), and returns the combined stdout+stderr with the
// command's error.
//
// When the sandbox itself did not apply, the error is a *SetupError instead.
// Surface that as an error, not as the tool result: the command did not run
// confined, or did not run at all.
func (s *Sandbox) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := s.Command(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	// caged closes every descriptor above stderr, so only this pipe reaches
	// the command. Run owns ExtraFiles.
	cmd.ExtraFiles = []*os.File{pw}

	report := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(pr)
		report <- string(b)
	}()

	out, runErr := cmd.CombinedOutput()
	// Our copy of the write end is what holds the reader open. The child's
	// copy dies with FD_CLOEXEC when caged execs the command.
	pw.Close()
	if msg := <-report; msg != "" {
		return out, &SetupError{Code: exitCode(runErr), Output: msg}
	}
	return out, runErr
}

// RunIO is Run with a stdin payload and stdout/stderr kept apart. It serves
// llm-tools, which answers one JSON request with one JSON response, so the
// combined output of Run would corrupt the reply. The setup-failure contract
// is the same.
func (s *Sandbox) RunIO(ctx context.Context, dir, name string, stdin []byte, args ...string) (stdout, stderr []byte, err error) {
	cmd := s.Command(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	pr, pw, perr := os.Pipe()
	if perr != nil {
		return nil, nil, perr
	}
	defer pr.Close()
	cmd.ExtraFiles = []*os.File{pw}

	report := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(pr)
		report <- string(b)
	}()

	runErr := cmd.Run()
	pw.Close()
	if msg := <-report; msg != "" {
		return out.Bytes(), errOut.Bytes(), &SetupError{Code: exitCode(runErr), Output: msg}
	}
	return out.Bytes(), errOut.Bytes(), runErr
}

// exitCode returns the process exit status, or -1 when it did not exit
// normally (killed by a signal, or never started).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// SetupError reports that the sandbox did not apply. Return it as an error,
// not as the tool result: a broken policy would look like a command that ran
// unconfined or failed for its own reasons.
type SetupError struct {
	// Code is the exit status caged exited with, or -1 when it did not exit
	// normally. Informative only: Output is what proves the failure.
	Code int
	// Output is what caged reported on the report descriptor.
	Output string
}

func (e *SetupError) Error() string {
	out := strings.TrimSpace(e.Output)
	switch e.Code {
	case ExitUsage:
		return fmt.Sprintf("sandbox: caged rejected the policy (exit %d): %s", e.Code, out)
	case ExitSetupFailed:
		return fmt.Sprintf("sandbox: caged could not apply the sandbox (exit %d): %s", e.Code, out)
	}
	return fmt.Sprintf("sandbox: caged reported a failure (exit %d): %s", e.Code, out)
}

// cagedEnv returns environ with the warnings switch appended, replacing any
// inherited value.
func cagedEnv(environ []string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		if strings.HasPrefix(kv, "CAGED_DISABLE_WARNINGS=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, disableWarnings)
}

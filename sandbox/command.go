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

// Exit codes caged uses for its own failures (see caged's main.c). They are a
// hint, never a proof: caged replaces itself with the command, so the command
// shares the status space and can exit with either number. What settles it is
// the report descriptor (see Sandbox.Run).
const (
	// ExitUsage means caged could not parse the policy: a bug on our side.
	ExitUsage = 2
	// ExitSetupFailed means caged failed to build or apply the sandbox, or
	// could not exec the command.
	ExitSetupFailed = 125
)

// ReportFD is the descriptor caged reports its own failures on (caged's
// --report-fd): the first one above stderr, so the command's stdio is
// untouched. caged marks it close-on-exec, so the command never sees it, and
// writes nothing to it when the setup succeeded.
const ReportFD = 3

// waitDelay bounds how long we wait for the command's I/O pipes to close after
// the process (group) was killed on cancellation, so a lingering grandchild
// holding the pipe cannot block the bridge forever.
const waitDelay = 5 * time.Second

// disableWarnings is set in caged's own environment so it does not print the
// "this kernel does not support feature X" warnings. caged prints them on every
// run, and a tool's output goes straight back to the model: repeating the same
// three lines on every shell/grep call would only burn tokens. The bridge's own
// run (the ceiling) still reports them once on stderr.
const disableWarnings = "CAGED_DISABLE_WARNINGS=1"

// Sandbox builds exec.Cmd values that run commands under a caged policy.
type Sandbox struct {
	// Bin is the path of the caged binary.
	Bin string
	// Policy is the sandbox built for every command.
	Policy Policy
}

// New validates bin and the policy and returns the sandbox. An empty bin or an
// invalid policy is an error: the caller is expected to fail loudly, not to
// fall back to unsandboxed execution.
func New(bin string, p Policy) (*Sandbox, error) {
	if bin == "" {
		return nil, errors.New("sandbox: empty caged binary path")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &Sandbox{Bin: bin, Policy: p}, nil
}

// Command returns a command that runs name with args under the sandbox policy:
//
//	caged <policy flags> --report-fd 3 -- name args...
//
// Prefer Run, which also wires the report descriptor and turns caged's own
// failures into a *SetupError. Use Command directly only when the caller needs
// the raw *exec.Cmd (and does not need to tell a broken sandbox from a command
// that exited with the same status).
//
// The command runs in its own process group, so cancelling the context kills
// the whole subtree (the tool's `bash -c` may have spawned background
// children: CommandContext alone would kill only caged, leaving them alive).
//
// Two caged limitations shape this:
//
//   - caged's close_range(3, ~0) discards inherited descriptors above stderr;
//     only pipes survive, and only the report descriptor is spared. Do not set
//     ExtraFiles on the returned command: Run uses it for the report pipe.
//   - caged replaces the environment with its base set unless KeepEnv is set,
//     so cmd.Env built by the caller is ignored on purpose.
func (s *Sandbox) Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	flags := s.Policy.Args()
	argv := make([]string, 0, len(flags)+4+len(args))
	argv = append(argv, flags...)
	argv = append(argv, "--report-fd", strconv.Itoa(ReportFD))
	argv = append(argv, "--", name)
	argv = append(argv, args...)

	cmd := exec.CommandContext(ctx, s.Bin, argv...)
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

// Run executes name with args under the sandbox policy, capturing the combined
// stdout+stderr, and returns it with the command's error (typically an
// *exec.ExitError carrying the command's own status).
//
// When caged reports that the sandbox itself did not apply, the error is a
// *SetupError instead: such a failure must never be handed to the model as the
// tool's output, because the command did not run confined (or not at all) and
// its status says nothing about the command. The decision is made from the
// report descriptor, never from the exit status: the command shares the status
// space with caged, so `ls` on a missing file (exit 2) or any program exiting
// 125 would otherwise be indistinguishable from a broken sandbox.
//
// dir is the working directory (empty means the bridge's own).
func (s *Sandbox) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := s.Command(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// The report pipe: caged writes its own diagnostics here and nothing when
	// the setup succeeded, so an empty read is the proof that whatever
	// happened was the command's doing.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	cmd.ExtraFiles = []*os.File{pw}

	report := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(pr)
		report <- string(b)
	}()

	out, runErr := cmd.CombinedOutput()
	// Our copy of the write end is what keeps the reader from seeing EOF; the
	// child's copy dies with FD_CLOEXEC as soon as caged execs the command.
	pw.Close()
	if msg := <-report; msg != "" {
		return out, &SetupError{Code: exitCode(runErr), Output: msg}
	}
	return out, runErr
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

// SetupError reports that the sandbox itself failed instead of the command. It
// must never be returned to the model as the tool's output: a broken policy or
// a sandbox that did not apply would silently look like a command that ran
// unconfined (or that failed for its own reasons).
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

// cagedEnv returns environ plus the switch that silences caged's ABI warnings,
// replacing any inherited value so the variable appears only once.
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

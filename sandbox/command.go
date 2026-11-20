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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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

// pipe is os.Pipe, kept behind a variable so the tests can exercise the
// failure path: a process out of descriptors has no stream to read.
var streamPipe = os.Pipe

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

// StreamLimits bounds one RunStream call. A zero field takes the default.
type StreamLimits struct {
	// Idle is how long the command may go without printing a value.
	Idle time.Duration
	// Total caps the whole call, from the start to the last value.
	Total time.Duration
	// MaxBytes caps what the command may print on stdout.
	MaxBytes int64
	// ExitGrace is how long the command gets to exit on its own after it
	// closed stdout, before the group is killed.
	ExitGrace time.Duration
}

// Default stream limits: long enough for a slow search, short enough that a
// hung server does not park the turn.
const (
	DefaultStreamIdle      = 20 * time.Second
	DefaultStreamTotal     = 5 * time.Minute
	DefaultStreamBytes     = 1 << 20
	DefaultStreamExitGrace = 2 * time.Second
)

// The limits that stop a stream early. None of them means the call failed: the
// caller keeps what came before and says why it stopped.
var (
	ErrStreamIdle  = errors.New("sandbox: the command went idle")
	ErrStreamTotal = errors.New("sandbox: the command hit the total deadline")
	ErrStreamBytes = errors.New("sandbox: the command went over the byte limit")
)

// withDefaults fills the zero fields.
func (l StreamLimits) withDefaults() StreamLimits {
	if l.Idle <= 0 {
		l.Idle = DefaultStreamIdle
	}
	if l.Total <= 0 {
		l.Total = DefaultStreamTotal
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultStreamBytes
	}
	if l.ExitGrace <= 0 {
		l.ExitGrace = DefaultStreamExitGrace
	}
	return l
}

// RunStream runs name with args, writes stdin to it, and calls onValue for each
// JSON value it prints on stdout. It returns when the command exits, when the
// stream goes idle or over a limit, or when ctx is cancelled.
//
// The process is not killed for answering: a one-shot command exits at EOF of
// its stdin, and one that closed stdout gets ExitGrace to exit. Past that, and
// on cancellation or any limit, the process group is killed.
//
// A *SetupError means the sandbox did not apply. Return it as an error, not as
// the tool result.
func (s *Sandbox) RunStream(ctx context.Context, dir, name string, stdin []byte, limits StreamLimits, onValue func(json.RawMessage) error, args ...string) error {
	cmd := s.Command(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	pr, pw, err := streamPipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{pw}
	return stream(ctx, cmd, stdin, limits, onValue, pr, pw)
}

// Stream is RunStream for a command the caller built, the way the tools run
// with -disable-sandbox. There is no report descriptor to watch.
func Stream(ctx context.Context, cmd *exec.Cmd, stdin []byte, limits StreamLimits, onValue func(json.RawMessage) error) error {
	return stream(ctx, cmd, stdin, limits, onValue, nil, nil)
}

// stream is the body both entry points share. reportR/reportW are the setup
// descriptor, nil when there is no caged in the middle.
func stream(ctx context.Context, cmd *exec.Cmd, stdin []byte, limits StreamLimits, onValue func(json.RawMessage) error, reportR, reportW *os.File) error {
	limits = limits.withDefaults()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// WaitDelay bounds the pipes: a grandchild that survives the kill and holds
	// one open must not hold Wait.
	cmd.WaitDelay = waitDelay

	outR, outW, err := streamPipe()
	if err != nil {
		if reportW != nil {
			reportW.Close()
		}
		return err
	}
	defer outR.Close()
	// An *os.File here means exec hands the descriptor over instead of copying,
	// so Wait does not depend on the pipe being drained.
	cmd.Stdout = outW
	var errBuf syncBuffer
	cmd.Stderr = &errBuf

	if err := cmd.Start(); err != nil {
		outW.Close()
		if reportW != nil {
			reportW.Close()
		}
		return err
	}
	// The child holds its own copies now, and our copy of the write end is what
	// would keep the reader from seeing EOF.
	outW.Close()
	if reportW != nil {
		reportW.Close()
	}

	// One Wait, in its own goroutine: the caller never pays for the process
	// dying, and nothing is left unreaped.
	go func() { _ = cmd.Wait() }()

	report := make(chan string, 1)
	if reportR != nil {
		defer reportR.Close()
		go func() {
			b, _ := io.ReadAll(reportR)
			report <- string(b)
		}()
	}

	events := make(chan json.RawMessage)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go readValues(outR, limits.MaxBytes, events, readErr, done)

	kill := func() {
		if cmd.Cancel != nil {
			_ = cmd.Cancel()
		}
	}
	// fail kills the group and carries what the command said on stderr, which
	// is the only clue when the answer was unreadable.
	fail := func(err error) error {
		kill()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if msg := strings.TrimSpace(errBuf.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}

	// finished reports whether a natural end has been fully seen: the reader
	// hit EOF, and the report descriptor closed. caged closes it at exec, or
	// writes to it and exits when the setup failed, so waiting for it is what
	// keeps a setup failure from being lost to the EOF that follows.
	finished := func() bool {
		return events == nil && (reportR == nil || report == nil)
	}

	idle := time.NewTimer(limits.Idle)
	defer idle.Stop()
	total := time.NewTimer(limits.Total)
	defer total.Stop()
	// Only armed after the stream ends, for a command that closed stdout and
	// did not exit.
	var grace <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case msg := <-report:
			report = nil
			if strings.TrimSpace(msg) != "" {
				kill()
				return &SetupError{Code: -1, Output: msg}
			}
			if finished() {
				return nil
			}
		case err := <-readErr:
			if !errors.Is(err, io.EOF) {
				return fail(err)
			}
			// Natural end of the stream. A command that exits at EOF is done;
			// one that closed stdout and stayed alive gets the grace.
			events = nil
			readErr = nil
			if finished() {
				return nil
			}
			grace = time.After(limits.ExitGrace)
		case raw := <-events:
			resetTimer(idle, limits.Idle)
			if onValue != nil {
				if err := onValue(raw); err != nil {
					return fail(err)
				}
			}
		case <-grace:
			kill()
			return drainVerdict(report)
		case <-idle.C:
			return fail(ErrStreamIdle)
		case <-total.C:
			return fail(ErrStreamTotal)
		}
	}
}

// readValues decodes JSON values from r and sends them to events until the
// input ends, a limit hits, or done is closed.
func readValues(r io.Reader, maxBytes int64, events chan<- json.RawMessage, readErr chan<- error, done <-chan struct{}) {
	dec := json.NewDecoder(&limitedReader{r: r, n: maxBytes})
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			// Buffered: the reader must not block on the way out when the
			// caller already returned.
			readErr <- err
			return
		}
		select {
		case events <- raw:
		case <-done:
			return
		}
	}
}

// reportDrain bounds the last look at the report, taken after the group was
// killed.
const reportDrain = 200 * time.Millisecond

// drainVerdict returns the setup error caged reported, if any, once the group
// is gone. A nil channel, an empty report and silence all mean the setup
// applied.
func drainVerdict(report <-chan string) error {
	if report == nil {
		return nil
	}
	select {
	case msg := <-report:
		if strings.TrimSpace(msg) != "" {
			return &SetupError{Code: -1, Output: msg}
		}
	case <-time.After(reportDrain):
	}
	return nil
}

// resetTimer restarts a timer that may or may not have fired.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// limitedReader reports the cap as its own error, so a command that keeps
// printing is stopped instead of being read to the end.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, ErrStreamBytes
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// syncBuffer collects stderr: exec writes from its own goroutine, and the error
// path reads while the process may still be running.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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

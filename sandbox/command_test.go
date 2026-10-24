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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCaged writes a shell script that stands in for the caged binary and
// returns its path. body runs with "$@" set to everything after the "--"
// separator (all the policy flags are simply discarded).
func fakeCaged(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "caged")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\n[ $# -gt 0 ] && shift\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCommandArgs checks the argv the sandbox builds: policy flags, then "--",
// then the command and its arguments.
func TestCommandArgs(t *testing.T) {
	bin := fakeCaged(t, `for a in "$@"; do printf '%s\n' "$a"; done`)
	sb, err := New(bin, Policy{ReadOnly: []string{"/usr"}, ReadWrite: []string{"/dev/null"}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := sb.Command(context.Background(), "bash", "-c", "echo hi")
	// The policy flags must come first, then the separator and the command.
	argv := cmd.Args
	if len(argv) < 8 {
		t.Fatalf("unexpected argv: %v", argv)
	}
	if argv[0] != bin || argv[1] != "--ro" || argv[2] != "/usr" || argv[3] != "--rw" || argv[4] != "/dev/null" {
		t.Errorf("unexpected policy flags: %v", argv)
	}
	sep := -1
	for i, a := range argv {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("missing -- separator in %v", argv)
	}
	want := []string{"bash", "-c", "echo hi"}
	got := argv[sep+1:]
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("command: got %v want %v", got, want)
	}

	// The wrapper must still run the command (a real caged execvpe's it).
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v (%s)", err, out)
	}
}

// TestCommandRunsCommand checks that the fake wrapper actually executes the
// command it is given.
func TestCommandRunsCommand(t *testing.T) {
	bin := fakeCaged(t, `exec "$@"`)
	sb, err := New(bin, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := sb.Command(context.Background(), "bash", "-c", "printf ok").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("output: got %q want %q", out, "ok")
	}
}

// TestNewRejectsBadInput checks the constructor fails loudly instead of
// silently running unsandboxed.
func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("", Policy{}); err == nil {
		t.Error("expected error for empty binary")
	}
	if _, err := New("/bin/true", Policy{Env: []string{"BAD"}}); err == nil {
		t.Error("expected error for invalid policy")
	}
}

// TestRunCommandStatusIsNotASetupError is the regression that motivated the
// report descriptor: caged execs the command, so the command shares the status
// space. A command exiting 2 or 125 is *not* a broken sandbox.
func TestRunCommandStatusIsNotASetupError(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{0, 1, 2, 3, 125, 127} {
		out, err := sb.Run(context.Background(), "", "bash", "-c", fmt.Sprintf("exit %d", code))
		var se *SetupError
		if errors.As(err, &se) {
			t.Errorf("exit %d: the command's status was taken for a sandbox failure: %v", code, se)
		}
		if code == 0 && err != nil {
			t.Errorf("exit 0: unexpected error %v", err)
		}
		if code != 0 {
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != code {
				t.Errorf("exit %d: got %v (output %q)", code, err, out)
			}
		}
	}
}

// TestRunReportsSetupFailure covers the other half: when caged writes to the
// report descriptor, the failure is the sandbox's, whatever the command did.
func TestRunReportsSetupFailure(t *testing.T) {
	sb, err := New(fakeCaged(t, `printf 'error allowing read access\n' >&3; exit 125`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sb.Run(context.Background(), "", "bash", "-c", "true")
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("expected a *SetupError, got %v", err)
	}
	if se.Code != ExitSetupFailed {
		t.Errorf("code: got %d want %d", se.Code, ExitSetupFailed)
	}
	if !strings.Contains(se.Output, "error allowing read access") {
		t.Errorf("output: %q", se.Output)
	}
}

// TestRunSetupFailureOverridesCleanExit: a report means the sandbox did not
// apply even when the status looks perfectly normal.
func TestRunSetupFailureOverridesCleanExit(t *testing.T) {
	sb, err := New(fakeCaged(t, `printf 'caged: boom\n' >&3; exit 0`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Run(context.Background(), "", "bash", "-c", "true"); err == nil {
		t.Fatal("expected the report to make the run fail")
	}
}

// TestRunSilentWithoutReport checks that a clean run leaves the descriptor
// empty and returns the output untouched.
func TestRunSilentWithoutReport(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out, err := sb.Run(context.Background(), dir, "bash", "-c", "pwd; printf oops >&2")
	if err != nil {
		t.Fatalf("run: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), dir) {
		t.Errorf("output does not start in %q: %q", dir, out)
	}
	if !strings.Contains(string(out), "oops") {
		t.Errorf("stderr was not captured: %q", out)
	}
}

// TestSetupErrorMessage checks the messages mention the cause and carry what
// caged reported.
func TestSetupErrorMessage(t *testing.T) {
	usage := (&SetupError{Code: ExitUsage, Output: "unknown option\n"}).Error()
	if !strings.Contains(usage, "policy") || !strings.Contains(usage, "unknown option") {
		t.Errorf("usage message: %q", usage)
	}
	setup := (&SetupError{Code: ExitSetupFailed, Output: "error execvpe: EACCES"}).Error()
	if !strings.Contains(setup, "apply") || !strings.Contains(setup, "EACCES") {
		t.Errorf("setup message: %q", setup)
	}
	other := (&SetupError{Code: -1, Output: "boom"}).Error()
	if !strings.Contains(other, "-1") || !strings.Contains(other, "boom") {
		t.Errorf("other message: %q", other)
	}
}

// TestCancelKillsProcessGroup checks that cancelling the context kills the
// whole subtree, not only the wrapper: a `bash -c` with a background child
// must not survive the cancellation.
func TestCancelKillsProcessGroup(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := sb.Command(ctx, "bash", "-c", "sleep 30 & echo started; wait")

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := cmd.CombinedOutput()
		done <- err
	}()

	// Give the shell time to fork the background sleep.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Errorf("cancellation took %v", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("command did not return after cancellation")
	}
}

// TestCancelWithoutProcessIsANoop checks Cancel is safe when the process never
// started (cmd.Process is nil): there is no group to kill, and that is not an
// error — CommandContext would not have a process to kill either.
func TestCancelWithoutProcessIsANoop(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	cmd := sb.Command(context.Background(), "true")
	if err := cmd.Cancel(); err != nil {
		t.Errorf("cancel without a process: %v", err)
	}
}

// TestCancelAfterExitIsANoop checks that cancelling an already finished
// process is not an error: kill(-pgid) returns ESRCH (the group is gone), and
// it is the call to Wait that settles the result.
func TestCancelAfterExitIsANoop(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	cmd := sb.Command(context.Background(), "true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running true: %v", err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Errorf("cancel after exit: %v", err)
	}
}

// functionalCaged picks the caged binary for the functional test: CAGED_BIN
// when it is set, otherwise caged from PATH. The test is skipped when caged is
// missing, or when it cannot be executed at all (EACCES, ENOEXEC, ENOENT): a
// caged outside the policy the test itself runs under is not a caged failure,
// so the test has nothing to say about it.
func functionalCaged(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CAGED_BIN")
	if bin == "" {
		var err error
		bin, err = exec.LookPath("caged")
		if err != nil {
			t.Skip("caged not found in PATH and CAGED_BIN unset")
		}
	}
	// Prove the binary runs before the test body interprets its results. A
	// non-zero exit is fine (an older caged may not know --version); only a
	// failure to start at all means caged is unusable here.
	if err := exec.Command(bin, "--version").Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Skipf("cannot execute caged %s: %v", bin, err)
		}
	}
	return bin
}

// TestFunctionalDeniesRead is the single test against the real kernel: it
// needs the caged binary installed, so it is skipped otherwise.
func TestFunctionalDeniesRead(t *testing.T) {
	bin := functionalCaged(t)
	sb, err := New(bin, Policy{ReadOnly: []string{"/usr", "/bin", "/lib", "/lib64", "/etc"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Reading a world-readable file inside the policy works.
	out, err := sb.Run(ctx, "", "cat", "/etc/hostname")
	if err != nil {
		t.Fatalf("cat /etc/hostname: %v (%s)", err, out)
	}

	// Reading a file outside the policy fails with EACCES, and that is the
	// command's own failure: the report descriptor stays empty.
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = sb.Run(ctx, "", "cat", secret)
	if err == nil {
		t.Fatalf("expected EACCES reading %s, got %q", secret, out)
	}
	var se *SetupError
	if errors.As(err, &se) {
		t.Fatalf("sandbox failed to apply: %v", se)
	}
	// The message is localized, so check the exit status instead of the text.
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() == 0 {
		t.Errorf("unexpected exit error: %v", err)
	}

	// The real caged owns the report descriptor: the command never sees it.
	if _, err := sb.Run(ctx, "", "bash", "-c", "cat <&3"); err == nil {
		t.Error("the command inherited the report descriptor")
	}

	// A command that legitimately exits 2 — `ls` on a missing file — is the
	// command's own failure. This is the regression the report descriptor
	// exists for: the status alone used to be taken for a broken policy.
	out, err = sb.Run(ctx, "", "ls", filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected ls on a missing file to fail, got %q", out)
	}
	if errors.As(err, &se) {
		t.Fatalf("ls exit 2 was taken for a sandbox failure: %v", se)
	}
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 2 {
		t.Errorf("expected exit 2 from ls, got %v", err)
	}

	// Without the network flags, creating a TCP socket must fail (seccomp).
	if out, err := sb.Run(ctx, "", "bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/9"); err == nil {
		t.Errorf("expected network to be denied, got %q", out)
	}
}

// TestCommandSilencesCagedWarnings checks the warning switch is handed to caged
// exactly once, even when it is already in the environment.
func TestCommandSilencesCagedWarnings(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAGED_DISABLE_WARNINGS", "0")
	cmd := sb.Command(context.Background(), "true")
	n := 0
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "CAGED_DISABLE_WARNINGS=") {
			n++
			if kv != disableWarnings {
				t.Errorf("unexpected value: %q", kv)
			}
		}
	}
	if n != 1 {
		t.Errorf("CAGED_DISABLE_WARNINGS appears %d times", n)
	}
}

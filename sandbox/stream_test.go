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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// collect returns an onValue that appends the `text` field of each value.
func collect(t *testing.T, out *[]string) func(json.RawMessage) error {
	t.Helper()
	return func(raw json.RawMessage) error {
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		*out = append(*out, v.Text)
		return nil
	}
}

// waitGone waits until pid is no longer alive, which is how a killed group is
// told apart from a group that was only abandoned.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process %d is still alive", pid)
}

// TestRunStreamReadsValues covers the happy path: the command reads the
// request, prints one value per line and exits at EOF of its stdin.
func TestRunStreamReadsValues(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	script := `req=$(cat)
printf '{"text":"one"}\n'
printf '{"text":"two"}\n'`
	var got []string
	err = sb.RunStream(context.Background(), "", "bash", []byte("request\n"),
		StreamLimits{Idle: 5 * time.Second, Total: 10 * time.Second}, collect(t, &got), "-c", script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("values: got %v", got)
	}
}

// TestRunStreamIdleKillsGroup: a command that prints and then hangs is stopped
// by the idle limit, and its whole group is killed, child included.
func TestRunStreamIdleKillsGroup(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := `printf '{"text":"first"}\n'
sleep 30 &
echo $! > ` + pidFile + `
wait`
	var got []string
	start := time.Now()
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 200 * time.Millisecond, Total: 30 * time.Second}, collect(t, &got), "-c", script)
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("expected ErrStreamIdle, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("idle stop took %v", elapsed)
	}
	if len(got) != 1 || got[0] != "first" {
		t.Errorf("values before the stop: got %v", got)
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr == nil {
		if pid, convErr := strconv.Atoi(string(data)); convErr == nil {
			waitGone(t, pid)
		}
	}
}

// TestRunStreamTotalLimit: a command that keeps talking is cut at the total
// deadline instead of running until the turn ends.
func TestRunStreamTotalLimit(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	script := `i=0
while true; do printf '{"text":"tick"}\n'; i=$((i+1)); sleep 0.05; done`
	var got []string
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 10 * time.Second, Total: 300 * time.Millisecond}, collect(t, &got), "-c", script)
	if !errors.Is(err, ErrStreamTotal) {
		t.Fatalf("expected ErrStreamTotal, got %v", err)
	}
	if len(got) == 0 {
		t.Error("no value arrived before the deadline")
	}
}

// TestRunStreamByteLimit: output past the cap stops the call instead of
// growing without bound.
func TestRunStreamByteLimit(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	script := `printf '{"text":"0123456789012345678901234567890123456789"}\n'`
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 5 * time.Second, Total: 5 * time.Second, MaxBytes: 16},
		func(json.RawMessage) error { return nil }, "-c", script)
	if !errors.Is(err, ErrStreamBytes) {
		t.Fatalf("expected ErrStreamBytes, got %v", err)
	}
}

// TestRunStreamCancel: cancelling the context returns without waiting for the
// process, and kills the group.
func TestRunStreamCancel(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sb.RunStream(ctx, "", "bash", nil,
			StreamLimits{Idle: 30 * time.Second, Total: 60 * time.Second},
			func(json.RawMessage) error { return nil }, "-c", "sleep 30")
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("cancellation took %v", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunStream did not return after cancellation")
	}
}

// TestRunStreamSetupFailure: the report descriptor wins over whatever the
// command printed, because a policy that did not apply means nothing ran
// confined.
func TestRunStreamSetupFailure(t *testing.T) {
	sb, err := New(fakeCaged(t, `printf '{"text":"hi"}\n'; printf 'caged: boom' >&3; exit 125`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 5 * time.Second, Total: 5 * time.Second},
		func(json.RawMessage) error { return nil }, "-c", "true")
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("expected a *SetupError, got %v", err)
	}
	if se.Code != -1 || !strings.Contains(se.Output, "boom") {
		t.Errorf("setup error: %+v", se)
	}
}

// TestRunStreamSetupFailureWithoutOutput covers the race the report and the EOF
// run: with nothing on stdout, the setup failure must not be lost to the EOF
// that follows it.
func TestRunStreamSetupFailureWithoutOutput(t *testing.T) {
	sb, err := New(fakeCaged(t, `printf 'caged: boom' >&3; exit 125`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	err = sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{},
		func(json.RawMessage) error { return nil }, "-c", "true")
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("expected a *SetupError, got %v", err)
	}
	if !strings.Contains(se.Output, "boom") {
		t.Errorf("setup error: %+v", se)
	}
}

// TestRunStreamExitGrace: a command that closes stdout and stays alive is
// killed after the grace, and the values it printed are kept.
func TestRunStreamExitGrace(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	script := `printf '{"text":"done"}\n'
exec 1>&-
sleep 30`
	var got []string
	start := time.Now()
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 10 * time.Second, Total: 20 * time.Second, ExitGrace: 200 * time.Millisecond},
		collect(t, &got), "-c", script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("exit grace took %v", elapsed)
	}
	if len(got) != 1 || got[0] != "done" {
		t.Errorf("values: got %v", got)
	}
}

// TestStreamWithoutSandbox covers the direct path: same limits, no caged and
// therefore no report descriptor.
func TestStreamWithoutSandbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", `printf '{"text":"a"}\n'; printf '{"text":"b"}\n'`)
	var got []string
	if err := Stream(ctx, cmd, nil, StreamLimits{}, collect(t, &got)); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("values: got %v", got)
	}
}

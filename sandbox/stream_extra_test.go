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
	"strings"
	"testing"
	"time"
)

// TestRunStreamWorkingDir: the command starts in dir, so a relative path means
// what the caller meant.
func TestRunStreamWorkingDir(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	err = sb.RunStream(context.Background(), dir, "bash", nil, StreamLimits{},
		collect(t, &got), "-c", `printf '{"text":"%s"}\n' "$(pwd -P)"`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 1 || got[0] != real {
		t.Errorf("pwd: got %v want %q", got, real)
	}
}

// TestRunStreamStderrIsNotAValue: a command that chats on stderr still only
// produces values from stdout.
func TestRunStreamStderrIsNotAValue(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	err = sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{}, collect(t, &got),
		"-c", `printf 'a warning' >&2; printf '{"text":"clean"}\n'`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 1 || got[0] != "clean" {
		t.Errorf("values: got %v", got)
	}
}

// TestRunStreamUnreadableOutputCarriesStderr: when the answer makes no sense,
// what the command said on stderr is the only clue, so it travels with the
// error.
func TestRunStreamUnreadableOutputCarriesStderr(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	err = sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{},
		func(json.RawMessage) error { return nil },
		"-c", `printf 'oops' >&2; sleep 0.1; printf 'not json\n'`)
	if err == nil {
		t.Fatal("expected an error for unreadable output")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("error without stderr: %v", err)
	}
}

// TestRunStreamStartFailure: a binary that cannot even start closes the report
// descriptor and returns the start error.
func TestRunStreamStartFailure(t *testing.T) {
	sb := &Sandbox{Bin: filepath.Join(t.TempDir(), "missing")}
	err := sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{},
		func(json.RawMessage) error { return nil }, "-c", "true")
	if err == nil {
		t.Fatal("expected a start error")
	}
}

// TestResetTimerAfterFire covers the branch where the timer already fired and
// its value is still in the channel.
func TestResetTimerAfterFire(t *testing.T) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	time.Sleep(20 * time.Millisecond)
	resetTimer(timer, 10*time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(2 * time.Second):
		t.Fatal("the timer was not restarted")
	}
}

// TestResetTimerAfterDrain covers the other fired branch: the timer expired
// and the value was already taken, so Stop reports false with an empty
// channel and there is nothing left to drain.
func TestResetTimerAfterDrain(t *testing.T) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	<-timer.C
	resetTimer(timer, 10*time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(2 * time.Second):
		t.Fatal("the timer was not restarted")
	}
}

// TestDrainVerdict covers the last look at the report: a failure message, an
// empty one, no descriptor, and a descriptor that never answers.
func TestDrainVerdict(t *testing.T) {
	bad := make(chan string, 1)
	bad <- "boom"
	err := drainVerdict(bad)
	var se *SetupError
	if !errors.As(err, &se) || !strings.Contains(se.Output, "boom") {
		t.Errorf("failure report: %v", err)
	}

	empty := make(chan string, 1)
	empty <- ""
	if err := drainVerdict(empty); err != nil {
		t.Errorf("empty report: %v", err)
	}

	if err := drainVerdict(nil); err != nil {
		t.Errorf("nil report: %v", err)
	}

	// Nothing ever arrives: the look is bounded, not a wait.
	start := time.Now()
	if err := drainVerdict(make(chan string)); err != nil {
		t.Errorf("silent report: %v", err)
	}
	if elapsed := time.Since(start); elapsed < reportDrain {
		t.Errorf("the look was not bounded: %v", elapsed)
	}
}

// TestPipeFailure: a process out of descriptors fails the call instead of
// running with no stream to read.
func TestPipeFailure(t *testing.T) {
	original := streamPipe
	streamPipe = func() (*os.File, *os.File, error) {
		return nil, nil, errors.New("out of descriptors")
	}
	defer func() { streamPipe = original }()

	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	err = sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{},
		func(json.RawMessage) error { return nil }, "-c", "true")
	if err == nil {
		t.Error("RunStream without a pipe: expected an error")
	}

	cmd := exec.CommandContext(context.Background(), "true")
	if err := Stream(context.Background(), cmd, nil, StreamLimits{}, nil); err == nil {
		t.Error("Stream without a pipe: expected an error")
	}

	// With a report descriptor, the failure closes it too.
	reportR, reportW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	defer reportR.Close()
	cmd = exec.CommandContext(context.Background(), "true")
	if err := stream(context.Background(), cmd, nil, StreamLimits{}, nil, reportR, reportW); err == nil {
		t.Error("stream without a pipe: expected an error")
	}
}

// TestReadValuesStopsWhenDone: once the caller is gone, the reader returns
// instead of blocking on a value nobody will take.
func TestReadValuesStopsWhenDone(t *testing.T) {
	events := make(chan json.RawMessage)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	close(done)
	readValues(strings.NewReader(`{"a":1} {"b":2}`), DefaultStreamBytes, events, readErr, done)
	select {
	case err := <-readErr:
		t.Fatalf("the reader should have stopped at done, got %v", err)
	default:
	}
}

// TestRunStreamOnValueError: a consumer that gives up ends the stream, and its
// error is what the caller sees.
func TestRunStreamOnValueError(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("enough")
	err = sb.RunStream(context.Background(), "", "bash", nil, StreamLimits{},
		func(json.RawMessage) error { return stop },
		"-c", `printf '{"text":"a"}\n'; sleep 30`)
	if !errors.Is(err, stop) {
		t.Fatalf("expected the consumer error, got %v", err)
	}
}

// TestRunStreamReportAfterEOF covers the other order of the same two endings:
// the stream closes first and the report descriptor closes after it.
//
// caged keeps the descriptor until it exits, so a command that closes stdout
// and lives a moment longer delivers the EOF before the empty report. The call
// has to end on that report instead of waiting out the grace, which is the
// order a loaded CI machine gets and a fast one usually does not.
func TestRunStreamReportAfterEOF(t *testing.T) {
	sb, err := New(fakeCaged(t, `exec "$@"`), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	script := `printf '{"text":"x"}\n'
exec 1>&-
sleep 0.3`
	var got []string
	start := time.Now()
	err = sb.RunStream(context.Background(), "", "bash", nil,
		StreamLimits{Idle: 30 * time.Second, Total: 60 * time.Second, ExitGrace: 10 * time.Second},
		collect(t, &got), "-c", script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The grace is ten seconds and the command lives 300ms, so a call that
	// waited for the grace instead of the report would show it here.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the empty report did not end the call, took %v", elapsed)
	}
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("values: got %v", got)
	}
}

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

package logger

import (
	"bytes"
	"strings"
	"testing"
)

func setLevelForTest(t *testing.T, level Level) {
	t.Helper()
	old := GetLogLevel()
	SetLogLevel(level)
	t.Cleanup(func() {
		SetLogLevel(old)
	})
}

func TestTracef(t *testing.T) {
	setLevelForTest(t, LevelTrace)
	var buf bytes.Buffer
	oldWriter := traceLogger.Writer()
	traceLogger.SetOutput(&buf)
	defer func() { traceLogger.SetOutput(oldWriter) }()

	Tracef("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[TRACE] hello" {
		t.Fatalf("got %q want %q", got, "[TRACE] hello")
	}
}

func TestDebugf(t *testing.T) {
	setLevelForTest(t, LevelDebug)
	var buf bytes.Buffer
	oldWriter := debugLogger.Writer()
	debugLogger.SetOutput(&buf)
	defer func() { debugLogger.SetOutput(oldWriter) }()

	Debugf("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[DEBUG] hello" {
		t.Fatalf("got %q want %q", got, "[DEBUG] hello")
	}
}

func TestInfof(t *testing.T) {
	setLevelForTest(t, LevelInfo)
	var buf bytes.Buffer
	oldWriter := infoLogger.Writer()
	infoLogger.SetOutput(&buf)
	defer func() { infoLogger.SetOutput(oldWriter) }()

	Infof("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[INFO] hello" {
		t.Fatalf("got %q want %q", got, "[INFO] hello")
	}
}

func TestWarningf(t *testing.T) {
	setLevelForTest(t, LevelWarning)
	var buf bytes.Buffer
	oldWriter := warningLogger.Writer()
	warningLogger.SetOutput(&buf)
	defer func() { warningLogger.SetOutput(oldWriter) }()

	Warningf("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[WARNING] hello" {
		t.Fatalf("got %q want %q", got, "[WARNING] hello")
	}
}

func TestErrorf(t *testing.T) {
	setLevelForTest(t, LevelError)
	var buf bytes.Buffer
	oldWriter := errorLogger.Writer()
	errorLogger.SetOutput(&buf)
	defer func() { errorLogger.SetOutput(oldWriter) }()

	Errorf("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[ERROR] hello" {
		t.Fatalf("got %q want %q", got, "[ERROR] hello")
	}
}

func TestSetLogLevelStrInvalid(t *testing.T) {
	if err := SetLogLevelStr("bad"); err == nil {
		t.Fatal("expected error for invalid log level")
	}
}

func TestSetOutput(t *testing.T) {
	setLevelForTest(t, LevelDebug)

	// Save the original writers so we can restore them afterwards.
	origTrace := traceLogger.Writer()
	origDebug := debugLogger.Writer()
	origInfo := infoLogger.Writer()
	origWarning := warningLogger.Writer()
	origError := errorLogger.Writer()
	t.Cleanup(func() {
		SetOutput(origTrace)
		traceLogger.SetOutput(origTrace)
		debugLogger.SetOutput(origDebug)
		infoLogger.SetOutput(origInfo)
		warningLogger.SetOutput(origWarning)
		errorLogger.SetOutput(origError)
	})

	var buf bytes.Buffer
	SetOutput(&buf)

	Debugf("hello")
	got := strings.TrimSpace(buf.String())
	if got != "[DEBUG] hello" {
		t.Fatalf("got %q want %q", got, "[DEBUG] hello")
	}
}

func TestSetLogLevelStrValid(t *testing.T) {
	tests := []struct {
		name  string
		level string
		want  Level
	}{
		{"trace", "trace", LevelTrace},
		{"debug", "debug", LevelDebug},
		{"info", "info", LevelInfo},
		{"warning", "warning", LevelWarning},
		{"error", "error", LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := SetLogLevelStr(tt.level); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if GetLogLevel() != tt.want {
				t.Fatalf("got level %d, want %d", GetLogLevel(), tt.want)
			}
		})
	}
}

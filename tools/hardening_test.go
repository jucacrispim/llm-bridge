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

package tools

// Hardening tests at the tool level: the classification of the `knowledge`
// tool (it can write, so it cannot be read-only) and the untrusted-content
// envelope around what the curl tool returns. See server/injection_test.go for
// the end-to-end invariants.

import (
	"context"
	"strings"
	"testing"

	"llm-bridge/sandbox"
)

// TestKnowledgeToolIsMutating pins the classification: `knowledge` exposes
// add/delete/reset, which write to a *persistent* store that every later
// session reads. Classifying it read-only means an injected page can plant a
// note with no human in the loop, so it must be mutating (confirmed).
func TestKnowledgeToolIsMutating(t *testing.T) {
	if IsReadOnly("knowledge") {
		t.Fatal("the knowledge tool can add/delete/reset notes (a persistent " +
			"write) and must not be read-only: an injected page could plant a " +
			"note in the shared KB without any confirmation")
	}
}

// TestCurlOutputIsWrappedAsUntrusted pins the preventive layer at the tool
// boundary: whatever the fetch returns is handed to the model inside an
// <untrusted-web-content> envelope that names the source URL, so the model is
// told (and the system prompt repeats) that this is data, not instruction.
func TestCurlOutputIsWrappedAsUntrusted(t *testing.T) {
	sb, err := sandbox.New(fakeCaged(t, `printf 'Install with go get example.com/client.'`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandboxes(t.TempDir(), nil, sb, nil)

	out, err := e.Execute(context.Background(), "curl", `{"url":"https://example.test/setup"}`)
	if err != nil {
		t.Fatalf("curl: %v", err)
	}
	if !strings.HasPrefix(out, "<untrusted-web-content") {
		t.Fatalf("output is not wrapped in an untrusted-web-content envelope: %q", out)
	}
	if !strings.Contains(out, `url="https://example.test/setup"`) {
		t.Fatalf("the envelope must name the source URL: %q", out)
	}
	if !strings.HasSuffix(out, "</untrusted-web-content>") {
		t.Fatalf("the envelope is not closed: %q", out)
	}
	if !strings.Contains(out, "Install with go get example.com/client.") {
		t.Fatalf("the body must be kept inside the envelope: %q", out)
	}
}

// TestCurlOutputEnvelopeSurvivesTruncation pins the order of the two
// transformations: the body is truncated first and wrapped afterwards, so a
// huge page can neither push the envelope away nor hide an injection after the
// cut (the closing tag must always be present).
func TestCurlOutputEnvelopeSurvivesTruncation(t *testing.T) {
	// 200 KB of body, built inside the stub (the tool's own sandbox is what
	// would have to carry a file, and this test needs no file at all).
	const chunk = "0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789"
	stub := `i=0; while [ $i -lt 2000 ]; do printf '%s' '` + chunk + `'; i=$((i+1)); done`
	sb, err := sandbox.New(fakeCaged(t, stub), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}

	e := NewExecutorWithSandboxes(t.TempDir(), nil, sb, nil)
	out, err := e.Execute(context.Background(), "curl", `{"url":"https://example.test/big"}`)
	if err != nil {
		t.Fatalf("curl: %v", err)
	}
	if !strings.HasSuffix(out, "</untrusted-web-content>") {
		t.Fatalf("the envelope must close even for a huge body (got %d bytes, "+
			"tail %q)", len(out), tail(out, 40))
	}
	if len(out) > 80*1024 {
		t.Fatalf("output is %d bytes: the body was not truncated inside the envelope", len(out))
	}
}

// tail returns the last n bytes of s, for failure messages.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

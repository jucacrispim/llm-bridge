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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureTempDirCreates checks the directory is created with 0700 and that a
// second call is a no-op (the bridge may run it again on every start).
func TestEnsureTempDirCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "llm-bridge")
	c := &sandboxConfig{tmpDir: dir}

	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("ensureTempDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions: got %o want 0700", perm)
	}
	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("second ensureTempDir: %v", err)
	}
}

// TestEnsureTempDirRejectsFile checks a path already taken by a regular file is
// a startup error instead of being silently ignored.
func TestEnsureTempDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{tmpDir: path}
	if err := c.ensureTempDir(); err == nil {
		t.Fatal("expected an error when the temp path is a file")
	}
}

// TestEnsureTempDirEmpty checks an empty -sandbox-tmp is a no-op (nothing is
// granted and nothing is created).
func TestEnsureTempDirEmpty(t *testing.T) {
	c := &sandboxConfig{}
	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("ensureTempDir: %v", err)
	}
}

// TestSharedPathsGrantsTempDir checks the temp directory is in the read-write
// list, which is what both layers (ceiling and tools) share.
func TestSharedPathsGrantsTempDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "llm-bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{tmpDir: dir}
	ro, rw := c.sharedPaths()

	if !contains(rw, dir) {
		t.Errorf("temp dir %q not granted read-write: %v", dir, rw)
	}
	if contains(ro, dir) {
		t.Errorf("temp dir %q granted read-only too: %v", dir, ro)
	}
	if !contains(rw, "/dev/null") {
		t.Errorf("/dev/null missing from rw: %v", rw)
	}
}

// TestSharedPathsMissingTempDirFiltered documents the ordering assumption:
// sharedPaths lists the temp directory unconditionally and existing() drops it
// when it is absent (caged exits 125 on a path it cannot open). validate()
// creates it before anything else, so in the real flow it is always there.
func TestSharedPathsMissingTempDirFiltered(t *testing.T) {
	c := &sandboxConfig{tmpDir: filepath.Join(t.TempDir(), "missing")}
	_, rw := c.sharedPaths()
	if !contains(rw, c.tmpDir) {
		t.Errorf("sharedPaths should list the temp dir unconditionally: %v", rw)
	}
	if contains(existing(rw), c.tmpDir) {
		t.Errorf("a missing temp dir should be filtered out: %v", existing(rw))
	}
}

// TestToolSandboxTempEnv checks the tools get the temporary-directory block on
// top of caged's base environment (they run without KeepEnv, so without this
// they would fall back to a denied /tmp).
func TestToolSandboxTempEnv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "llm-bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), tmpDir: dir}

	sb, err := c.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	for _, name := range tempEnvNames {
		want := name + "=" + dir
		if !contains(sb.Policy.Env, want) {
			t.Errorf("missing %q in policy env: %v", want, sb.Policy.Env)
		}
		// It must reach caged as --env, not only in the Go struct.
		if !contains(sb.Policy.Args(), want) {
			t.Errorf("missing %q in caged args: %v", want, sb.Policy.Args())
		}
	}
	if !contains(sb.Policy.ReadWrite, dir) {
		t.Errorf("temp dir not granted to the tools: %v", sb.Policy.ReadWrite)
	}
}

// TestTempEnvEmpty checks an empty temp dir produces no entries at all (not
// "TMPDIR=", which would point the tools at the process's own cwd).
func TestTempEnvEmpty(t *testing.T) {
	if env := tempEnv(""); env != nil {
		t.Errorf("expected nil, got %v", env)
	}
}

// TestReexecEnvOverridesTempVars checks the re-executed bridge gets the temp
// block pointing at the granted directory, replacing whatever the parent
// exported (an inherited TMPDIR=/tmp would point at a denied path), while the
// rest of the environment survives untouched.
func TestReexecEnvOverridesTempVars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "llm-bridge")
	parent := []string{
		"FOO=bar",
		"TMPDIR=/tmp",
		"GOTMPDIR=/tmp",
		"TMP=/tmp",
		"TEMP=/tmp",
		"TMPDIR_OLD=/tmp", // a different variable, must survive
		sandboxedEnv + "=0",
	}
	env := reexecEnv(parent, dir)

	if !contains(env, "FOO=bar") {
		t.Errorf("inherited variable lost: %v", env)
	}
	if !contains(env, "TMPDIR_OLD=/tmp") {
		t.Errorf("lookalike variable was dropped: %v", env)
	}
	if contains(env, "TMPDIR=/tmp") || contains(env, "GOTMPDIR=/tmp") ||
		contains(env, "TMP=/tmp") || contains(env, "TEMP=/tmp") {
		t.Errorf("parent temp variables were not replaced: %v", env)
	}
	for _, name := range tempEnvNames {
		want := name + "=" + dir
		if n := count(env, want); n != 1 {
			t.Errorf("%q appears %d times, want 1: %v", want, n, env)
		}
	}
	if n := count(env, sandboxedEnv+"=1"); n != 1 {
		t.Errorf("guard appears %d times: %v", n, env)
	}
}

// TestReexecEnvKeepsParentTempVarsWithoutFlag checks that with no temp dir
// configured (empty -sandbox-tmp) the parent's variables are left alone.
func TestReexecEnvKeepsParentTempVarsWithoutFlag(t *testing.T) {
	env := reexecEnv([]string{"TMPDIR=/tmp"}, "")
	if !contains(env, "TMPDIR=/tmp") {
		t.Errorf("parent TMPDIR should survive: %v", env)
	}
}

// fakeCagedPath returns the path of a stub caged binary: enough for
// sandboxConfig.binary(), which only resolves the path.
func fakeCagedPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caged")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// contains reports whether list has exactly the entry s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// count returns how many entries of list equal s.
func count(list []string, s string) int {
	n := 0
	for _, v := range list {
		if strings.TrimSpace(v) == s {
			n++
		}
	}
	return n
}

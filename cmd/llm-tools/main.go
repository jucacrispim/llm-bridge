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

//go:build !test

// llm-tools runs the bridge's filesystem tools out of process, so the bridge
// itself never opens a path that came from the model.
//
// It has no policy of its own and none of the bridge's privileges: the bridge
// runs it under caged, with the same policy a shell command gets, and it only
// knows how to read, write, glob and search-and-replace. It says nothing about
// what it may touch — the kernel does, from the policy the bridge chose.
//
// The protocol is in the tools package (see tools/toolbox.go): one JSON request
// per line on stdin, one JSON response per line on stdout, until stdin closes.
// Running it by hand is not a sandbox bypass (there is no sandbox to bypass at
// that point, exactly as running `cat` by hand is not): it just does what you
// ask, with your own permissions.
package main

// notest

import (
	"fmt"
	"os"

	"llm-bridge/tools"
)

func main() {
	if err := tools.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "llm-tools: %v\n", err)
		os.Exit(1)
	}
}

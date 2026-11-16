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
// process itself never opens a path that came from the model.
//
// It holds no policy and no privilege: the bridge runs it under caged with the
// policy a shell command gets, and the kernel decides what it may touch. The
// protocol is in tools/toolbox.go and docs/source/tools.rst.
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

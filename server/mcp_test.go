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

package server

import (
	"bytes"
	"testing"

	"llm-bridge/llm"
	"llm-bridge/mcp"
)

// TestExecuteMCPToolCancelled: a turn cancelled while the server is answering
// stops the call at the next fragment and appends nothing to the history,
// because the assistant message was already sanitized away.
func TestExecuteMCPToolCancelled(t *testing.T) {
	st := &state{}
	st.sandboxes.MCP = mcp.NewManager([]*mcp.Server{{Name: "fake", Bin: fakeMCPServer(t)}})
	st.setCancelled(true)

	var out bytes.Buffer
	st.executeTool(llm.ToolCall{ID: "t1", Name: "mcp__fake__do", Arguments: "{}"}, &out)

	if len(st.history) != 0 {
		t.Errorf("history got a result for a cancelled turn: %+v", st.history)
	}
}

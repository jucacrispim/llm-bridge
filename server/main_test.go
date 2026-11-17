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
	"os"
	"testing"
	"time"
)

// TestMain freezes the turn clock, so the timings in turn_end are zero and the
// golden strings stay stable. Tests that measure time set their own clock.
func TestMain(m *testing.M) {
	frozen := time.Unix(0, 0)
	now = func() time.Time { return frozen }
	os.Exit(m.Run())
}

#!/bin/sh
#
# Copyright 2026 Juca Crispim <juca@poraodojuca.dev>
#
# This file is part of llm-bridge.
#
# llm-bridge is free software: you can redistribute it and/or modify
# it under the terms of the GNU General Public License as published by
# the Free Software Foundation, either version 3 of the License, or
# (at your option) any later version.
#
# llm-bridge is distributed in the hope that it will be useful,
# but WITHOUT ANY WARRANTY; without even the implied warranty of
# MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
# GNU General Public License for more details.
#
# You should have received a copy of the GNU General Public License
# along with llm-bridge. If not, see <http://www.gnu.org/licenses/>.

# Measures what the private namespaces do, against the real caged binary.
#
# Run it outside any cage. Inside one, unshare(CLONE_NEWUSER) fails with ENOSYS
# and the functional tests skip themselves, which this script reports as NOT
# MEASURED instead of passing quietly.

set -eu
cd "$(dirname "$0")/.."

if ! unshare -Ur true 2>/dev/null; then
    echo "warning: unshare(CLONE_NEWUSER) fails here, so the namespace checks will skip."
    echo "         A shell inside another cage cannot measure them. Run from the host."
    echo ""
fi

echo "== functional sandbox tests =="
# The status of the pipeline is tee's, so the log is what decides.
go test -count=1 -v -run 'TestFunctional' ./sandbox ./cmd/llm-bridge 2>&1 | tee /tmp/sandbox-e2e.log

if grep -q -- '--- FAIL' /tmp/sandbox-e2e.log; then
    echo ""
    echo "FAILED: a functional test did not pass. See /tmp/sandbox-e2e.log."
    exit 1
fi

if grep -q -- '--- SKIP' /tmp/sandbox-e2e.log; then
    echo ""
    echo "NOT MEASURED: at least one functional test skipped. See /tmp/sandbox-e2e.log."
    exit 1
fi

echo ""
echo "== packages the tools live in =="
go test -count=1 ./llm ./tools ./hooks

echo ""
echo "all measured"

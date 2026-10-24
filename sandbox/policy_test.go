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
	"reflect"
	"strings"
	"testing"
)

func TestPolicyArgs(t *testing.T) {
	tests := []struct {
		name   string
		policy Policy
		want   []string
	}{
		{
			name:   "zero value denies everything",
			policy: Policy{},
			want:   nil,
		},
		{
			name:   "files only",
			policy: Policy{ReadOnly: []string{"/usr", "/etc"}, ReadWrite: []string{"/tmp/x"}},
			want:   []string{"--ro", "/usr", "--ro", "/etc", "--rw", "/tmp/x"},
		},
		{
			name:   "network",
			policy: Policy{AllowNetworkSockets: true, ConnectPorts: []int{443, 0}},
			want: []string{
				"--allow-network-sockets",
				"--allow-connect-port", "443",
				"--allow-connect-port", "0",
			},
		},
		{
			name:   "connect tcp and bind tcp",
			policy: Policy{ConnectTCP: true, BindTCP: true},
			want:   []string{"--allow-connect-tcp", "--allow-bind-tcp"},
		},
		{
			name:   "unix sockets and bind ports",
			policy: Policy{AllowUnixSockets: true, BindPorts: []int{8080}},
			want:   []string{"--allow-unix-sockets", "--allow-bind-port", "8080"},
		},
		{
			name:   "env and limits",
			policy: Policy{KeepEnv: true, Env: []string{"A=1"}, MaxProcs: 10, RequireABI: 6},
			want:   []string{"--keep-env", "--env", "A=1", "--max-procs", "10", "--require-abi", "6"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.policy.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}
			if got := tt.policy.Args(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args:\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  Policy
		wantErr string
	}{
		{name: "ok", policy: Policy{}},
		{name: "zero limits are omitted", policy: Policy{MaxProcs: 0, RequireABI: 0}},
		{name: "connect tcp conflict", policy: Policy{ConnectTCP: true, ConnectPorts: []int{443}}, wantErr: "connect-tcp conflicts"},
		{name: "bind tcp conflict", policy: Policy{BindTCP: true, BindPorts: []int{80}}, wantErr: "bind-tcp conflicts"},
		{name: "connect port too big", policy: Policy{ConnectPorts: []int{70000}}, wantErr: "out of range"},
		{name: "connect port negative", policy: Policy{ConnectPorts: []int{-1}}, wantErr: "out of range"},
		{name: "bind port too big", policy: Policy{BindPorts: []int{70000}}, wantErr: "out of range"},
		{name: "env without equals", policy: Policy{Env: []string{"FOO"}}, wantErr: "NAME=VALUE"},
		{name: "negative max procs", policy: Policy{MaxProcs: -1}, wantErr: "max-procs"},
		{name: "negative require abi", policy: Policy{RequireABI: -1}, wantErr: "require-abi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

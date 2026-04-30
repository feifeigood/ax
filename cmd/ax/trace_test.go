// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"testing"

	"github.com/google/ax/proto"
)

func TestBuildExecTraces_PreservesOrderOfExecIDs(t *testing.T) {
	execIDs := []string{"root-exec", "child-exec-1", "child-exec-2"}

	// Events arrive in arbitrary order
	events := []*proto.ExecutionEvent{
		{ExecId: "child-exec-2"},
		{ExecId: "root-exec"},
		{ExecId: "child-exec-1"},
	}

	execs := buildExecTraces(execIDs, events)

	if len(execs) != 3 {
		t.Fatalf("expected 3 execution traces, got %d", len(execs))
	}

	// Verify order matches execIDs
	if execs[0].ExecID != "root-exec" {
		t.Errorf("expected execs[0] to be 'root-exec', got %s", execs[0].ExecID)
	}
	if execs[1].ExecID != "child-exec-1" {
		t.Errorf("expected execs[1] to be 'child-exec-1', got %s", execs[1].ExecID)
	}
	if execs[2].ExecID != "child-exec-2" {
		t.Errorf("expected execs[2] to be 'child-exec-2', got %s", execs[2].ExecID)
	}
}

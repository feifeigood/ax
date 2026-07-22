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

package antigravityinteractions

import "testing"

func TestWorkspaceSystemInstruction(t *testing.T) {
	t.Run("empty returns empty", func(t *testing.T) {
		for _, in := range []string{"", "   ", "\t\n"} {
			if got := WorkspaceSystemInstruction(in); got != "" {
				t.Errorf("WorkspaceSystemInstruction(%q) = %q, want empty", in, got)
			}
		}
	})

	t.Run("names the working directory", func(t *testing.T) {
		got := WorkspaceSystemInstruction("/durable/work")
		if got == "" {
			t.Fatal("WorkspaceSystemInstruction(/durable/work) = empty, want a snippet")
		}
		if want := "/durable/work"; !contains(got, want) {
			t.Errorf("WorkspaceSystemInstruction(%q) = %q, want it to mention %q", "/durable/work", got, want)
		}
	})

	t.Run("joins cleanly as a system-instruction pointer", func(t *testing.T) {
		// Empty workDir must not add a stray separator to an existing instruction.
		if got := JoinSystemInstruction("base", WorkspaceSystemInstruction("")); got != "base" {
			t.Errorf("JoinSystemInstruction with empty workspace = %q, want %q", got, "base")
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

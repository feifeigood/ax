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

package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/google/ax/internal/controller/eventlog/eventlogtest"
)

// agentfleet #223: actor_token is a reserved, transport-only key in the pushed
// harness_config. LogInputs must strip it from the human-readable copy it appends
// to the conversation log, so a per-conversation bearer credential never lands in
// the durable log — even when it is the only key present.
func TestLogInputsStripsActorToken(t *testing.T) {
	for _, tc := range []struct {
		name          string
		harnessConfig string
		wantKept      []string // keys that MUST remain in the logged config
	}{
		{"token alone", `{"actor_token":"afh_v1_secret.mac"}`, nil},
		{"token among config", `{"instructions":"be helpful","actor_token":"afh_v1_secret.mac","runtime":"pi"}`, []string{"instructions", "runtime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			el := &eventlogtest.MemoryEventLog{}
			l := newLogger(el, "conv-1", "harness-1")

			if _, err := l.LogInputs(context.Background(), nil, []byte(tc.harnessConfig)); err != nil {
				t.Fatalf("LogInputs: %v", err)
			}

			if len(el.AllEvents) != 1 {
				t.Fatalf("appended %d events, want 1", len(el.AllEvents))
			}
			cfg := el.AllEvents[0].GetHarnessConfig()
			if cfg == nil {
				t.Fatal("logged event has no harness config")
			}
			if _, ok := cfg.GetFields()["actor_token"]; ok {
				t.Fatal("actor_token leaked into the conversation log")
			}
			for _, k := range tc.wantKept {
				if _, ok := cfg.GetFields()[k]; !ok {
					t.Fatalf("non-secret key %q was dropped from the logged config", k)
				}
			}
		})
	}
}

// The token must not survive even as a substring anywhere in the logged config's
// text form — belt-and-suspenders against a future field carrying it verbatim.
func TestLogInputsLoggedConfigHasNoTokenSubstring(t *testing.T) {
	el := &eventlogtest.MemoryEventLog{}
	l := newLogger(el, "conv-1", "harness-1")
	if _, err := l.LogInputs(context.Background(), nil, []byte(`{"instructions":"hi","actor_token":"afh_v1_TOPSECRET.mac"}`)); err != nil {
		t.Fatalf("LogInputs: %v", err)
	}
	if got := el.AllEvents[0].GetHarnessConfig().String(); strings.Contains(got, "TOPSECRET") {
		t.Fatalf("token value leaked into logged config: %s", got)
	}
}

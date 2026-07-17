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
	"testing"

	"github.com/google/ax/internal/harness"
)

type dummyHarness struct{}

func (d *dummyHarness) Start(ctx context.Context, conversationID string, harnessConfig []byte) (harness.Execution, error) {
	return nil, nil
}

// drainableHarness implements the optional harness.Drainer capability so the
// registry test can assert that Close drains it.
type drainableHarness struct {
	dummyHarness
	shutdownCalls int
}

func (d *drainableHarness) Shutdown(ctx context.Context) {
	d.shutdownCalls++
}

// Close must invoke Shutdown on every registered harness that implements the
// Drainer capability so warm actors are suspended before the process exits.
// Harnesses without the capability are skipped without error.
func TestRegistry_CloseDrainsDrainerHarnesses(t *testing.T) {
	r := NewRegistry()
	drainable := &drainableHarness{}
	if err := r.RegisterHarness("warm", drainable); err != nil {
		t.Fatalf("RegisterHarness(warm): %v", err)
	}
	if err := r.RegisterHarness("plain", &dummyHarness{}); err != nil {
		t.Fatalf("RegisterHarness(plain): %v", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if drainable.shutdownCalls != 1 {
		t.Fatalf("Shutdown calls = %d, want 1", drainable.shutdownCalls)
	}
}

func TestRegistry_RegisterHarness(t *testing.T) {
	r := NewRegistry()
	h := &dummyHarness{}

	if err := r.RegisterHarness("antigravity", h); err != nil {
		t.Fatalf("RegisterHarness(valid id): %v", err)
	}

	// Duplicate id is rejected.
	if err := r.RegisterHarness("antigravity", h); err == nil {
		t.Error("expected error registering duplicate id, got nil")
	}

	// Invalid id is rejected.
	if err := r.RegisterHarness("bad id", h); err == nil {
		t.Error("expected error registering invalid id, got nil")
	}

	// Empty id is reserved for the default harness.
	if err := r.RegisterHarness("", h); err == nil {
		t.Error("expected error registering empty id, got nil")
	}
}

func TestRegistry_FindHarness(t *testing.T) {
	r := NewRegistry()
	h := &dummyHarness{}
	if err := r.RegisterHarness("antigravity", h); err != nil {
		t.Fatalf("RegisterHarness: %v", err)
	}

	if _, err := r.Harness("antigravity"); err != nil {
		t.Errorf("Harness(antigravity): %v", err)
	}
	if _, err := r.Harness("missing"); err == nil {
		t.Error("expected error looking up missing harness, got nil")
	}
}

func TestRegistry_SetDefaultHarness(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterHarness("antigravity", &dummyHarness{}); err != nil {
		t.Fatalf("RegisterHarness: %v", err)
	}

	// An unregistered id is rejected.
	if err := r.SetDefaultHarness("missing"); err == nil {
		t.Error("SetDefaultHarness(missing): expected error, got nil")
	}

	// A registered id becomes the default.
	if err := r.SetDefaultHarness("antigravity"); err != nil {
		t.Fatalf("SetDefaultHarness(antigravity): %v", err)
	}
	if r.defaultHarness != "antigravity" {
		t.Errorf("defaultHarness = %q, want %q", r.defaultHarness, "antigravity")
	}
}

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

package a2abridge

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestOverrideCardHosts_RewritesEverySupportedInterface(t *testing.T) {
	card := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			{URL: "http://localhost:8080/jsonrpc"},
			{URL: "http://localhost:8080/rest"},
			{URL: "http://localhost:50051"},
		},
	}
	if err := OverrideCardHosts(card, "http://10.0.1.5:8080"); err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	want := []string{
		"http://10.0.1.5:8080/jsonrpc",
		"http://10.0.1.5:8080/rest",
		"http://10.0.1.5:8080",
	}
	for i, iface := range card.SupportedInterfaces {
		if iface.URL != want[i] {
			t.Errorf("SupportedInterfaces[%d].URL = %q, want %q", i, iface.URL, want[i])
		}
	}
}

func TestOverrideCardHosts_PreservesSchemeAndPath(t *testing.T) {
	card := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			{URL: "https://example.com/v2/agent?foo=bar"},
		},
	}
	if err := OverrideCardHosts(card, "https://1.2.3.4:443"); err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	got := card.SupportedInterfaces[0].URL
	if got != "https://1.2.3.4:443/v2/agent?foo=bar" {
		t.Errorf("URL = %q, want scheme/path/query preserved with new host", got)
	}
}

func TestOverrideCardHosts_NilCardNoOp(t *testing.T) {
	if err := OverrideCardHosts(nil, "http://10.0.1.5:8080"); err != nil {
		t.Errorf("got err=%v, want nil for nil card", err)
	}
}

func TestOverrideCardHosts_EmptyInterfacesNoOp(t *testing.T) {
	card := &a2a.AgentCard{}
	if err := OverrideCardHosts(card, "http://10.0.1.5:8080"); err != nil {
		t.Errorf("got err=%v, want nil for empty interfaces", err)
	}
}

func TestOverrideCardHosts_RejectsAddrWithoutHost(t *testing.T) {
	card := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{{URL: "http://localhost:8080"}},
	}
	err := OverrideCardHosts(card, "")
	if err == nil {
		t.Fatal("got nil err, want non-nil for empty addr")
	}
	if !strings.Contains(err.Error(), "no host") {
		t.Errorf("err should mention missing host; got: %v", err)
	}
}

func TestOverrideCardHosts_RejectsMalformedURL(t *testing.T) {
	card := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			{URL: "://bad-url"},
		},
	}
	err := OverrideCardHosts(card, "http://10.0.1.5:8080")
	if err == nil {
		t.Fatal("got nil err, want non-nil for malformed URL")
	}
	if !strings.Contains(err.Error(), "SupportedInterfaces[0]") {
		t.Errorf("err should mention which interface failed; got: %v", err)
	}
}

func TestOverrideCardHosts_SkipsNilInterface(t *testing.T) {
	card := &a2a.AgentCard{
		SupportedInterfaces: []*a2a.AgentInterface{
			nil,
			{URL: "http://localhost:8080"},
		},
	}
	if err := OverrideCardHosts(card, "http://10.0.1.5:8080"); err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if card.SupportedInterfaces[1].URL != "http://10.0.1.5:8080" {
		t.Errorf("non-nil interface should be rewritten; got %q", card.SupportedInterfaces[1].URL)
	}
}

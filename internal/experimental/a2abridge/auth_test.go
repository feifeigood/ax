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
	"context"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/google/ax/internal/auth"
)

// cardWithAPIKey returns an AgentCard declaring a single APIKeySecurityScheme
// at the given location.
func cardWithAPIKey(name string, location a2a.APIKeySecuritySchemeLocation) *a2a.AgentCard {
	return &a2a.AgentCard{
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"apikey": a2a.APIKeySecurityScheme{Name: name, Location: location},
		},
	}
}

// ----- apiKeyHeaderFromCard -----

func TestApiKeyHeaderFromCard_Found(t *testing.T) {
	card := cardWithAPIKey("X-API-Key", a2a.APIKeySecuritySchemeLocationHeader)
	name, err := apiKeyHeaderFromCard(card)
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if name != "X-API-Key" {
		t.Errorf("name=%q, want %q", name, "X-API-Key")
	}
}

func TestApiKeyHeaderFromCard_NoMatchingScheme(t *testing.T) {
	cases := map[string]*a2a.AgentCard{
		"nil":      nil, // no card at all
		"empty":    {},  // no SecuritySchemes
		"cookie":   cardWithAPIKey("session", a2a.APIKeySecuritySchemeLocationCookie),
		"querystr": cardWithAPIKey("api_key", a2a.APIKeySecuritySchemeLocationQuery),
	}
	for label, card := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := apiKeyHeaderFromCard(card)
			if err == nil {
				t.Fatal("got nil err, want non-nil")
			}
			if !strings.Contains(err.Error(), "APIKeySecurityScheme") {
				t.Errorf("err should mention APIKeySecurityScheme; got: %v", err)
			}
		})
	}
}

// ----- NewInterceptor -----

func TestNewInterceptor_NilWhenEmpty(t *testing.T) {
	got, err := NewInterceptor(nil, auth.Auth{}, auth.Headers{})
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got != nil {
		t.Errorf("got non-nil interceptor, want nil for empty config")
	}
}

func TestNewInterceptor_PropagatesError(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc123")
	_, err := NewInterceptor(nil,
		auth.Auth{Type: "bearer", CredentialEnv: "MY_TOKEN"},
		auth.Headers{Literal: map[string]string{"Authorization": "Bearer manual"}},
	)
	if err == nil {
		t.Fatal("got nil err, want non-nil for collision")
	}
}

// ----- interceptor (CallInterceptor implementation) -----

func TestInterceptor_BeforeWritesToRequestServiceParams(t *testing.T) {
	in := &interceptor{
		headers: map[string]string{
			"Authorization":     "Bearer abc",
			"X-Organization-Id": "acme",
		},
	}
	req := &a2aclient.Request{ServiceParams: make(a2aclient.ServiceParams)}
	if _, _, err := in.Before(context.Background(), req); err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got := req.ServiceParams["Authorization"]; len(got) != 1 || got[0] != "Bearer abc" {
		t.Errorf("Authorization=%v, want [Bearer abc]", got)
	}
	if got := req.ServiceParams["X-Organization-Id"]; len(got) != 1 || got[0] != "acme" {
		t.Errorf("X-Organization-Id=%v, want [acme]", got)
	}
}

func TestInterceptor_BeforeOverwritesExistingValue(t *testing.T) {
	in := &interceptor{headers: map[string]string{"Authorization": "Bearer correct"}}
	req := &a2aclient.Request{ServiceParams: a2aclient.ServiceParams{
		"Authorization": []string{"Bearer stale"},
	}}
	if _, _, err := in.Before(context.Background(), req); err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got := req.ServiceParams["Authorization"]; len(got) != 1 || got[0] != "Bearer correct" {
		t.Errorf("Authorization=%v, want [Bearer correct] (overwrite)", got)
	}
}

// Ensure the unexported interceptor still implements the SDK's
// CallInterceptor interface. If the SDK changes its method set, this
// fails to compile rather than at runtime.
var _ a2aclient.CallInterceptor = (*interceptor)(nil)

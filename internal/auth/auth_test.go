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

package auth

import (
	"strings"
	"testing"
)

// ----- ResolveAuthHeader -----

func TestResolveAuthHeader_BearerSuccess(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc123")
	name, value, err := resolveAuthHeader(Auth{Type: "bearer", CredentialEnv: "MY_TOKEN"}, "")
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if name != "Authorization" {
		t.Errorf("name=%q, want %q", name, "Authorization")
	}
	if value != "Bearer abc123" {
		t.Errorf("value=%q, want %q", value, "Bearer abc123")
	}
}

func TestResolveAuthHeader_MissingCredentialEnvFieldErrors(t *testing.T) {
	for _, scheme := range []string{"bearer", "api_key"} {
		t.Run(scheme, func(t *testing.T) {
			_, _, err := resolveAuthHeader(Auth{Type: scheme}, "")
			if err == nil {
				t.Fatal("got nil err, want non-nil for missing credential_env")
			}
			if !strings.Contains(err.Error(), "credential_env") {
				t.Errorf("err should mention credential_env field; got: %v", err)
			}
		})
	}
}

func TestResolveAuthHeader_EnvVarUnsetErrors(t *testing.T) {
	t.Setenv("MY_CRED", "")
	for _, scheme := range []string{"bearer", "api_key"} {
		t.Run(scheme, func(t *testing.T) {
			// apiKeyHeader is "", but the env-empty check fires before
			// the api_key path even consults it.
			_, _, err := resolveAuthHeader(Auth{Type: scheme, CredentialEnv: "MY_CRED"}, "")
			if err == nil {
				t.Fatal("got nil err, want non-nil for unset env")
			}
			if !strings.Contains(err.Error(), "MY_CRED") {
				t.Errorf("err should mention env var; got: %v", err)
			}
		})
	}
}

func TestResolveAuthHeader_UnknownTypeErrors(t *testing.T) {
	t.Setenv("MY_CRED", "value")
	_, _, err := resolveAuthHeader(Auth{Type: "oauth2", CredentialEnv: "MY_CRED"}, "")
	if err == nil {
		t.Fatal("got nil err, want non-nil for unknown type")
	}
	if !strings.Contains(err.Error(), "oauth2") {
		t.Errorf("err should mention the unknown type; got: %v", err)
	}
}

func TestResolveAuthHeader_ApiKeyEmptyHeaderErrors(t *testing.T) {
	t.Setenv("MY_KEY", "k-secret")
	_, _, err := resolveAuthHeader(Auth{Type: "api_key", CredentialEnv: "MY_KEY"}, "")
	if err == nil {
		t.Fatal("got nil err, want non-nil for empty apiKeyHeader")
	}
	if !strings.Contains(err.Error(), "no header name") {
		t.Errorf("err should mention missing header name; got: %v", err)
	}
}

// ----- MergeAllHeaders -----

func TestMergeAllHeaders_LiteralOnly(t *testing.T) {
	got, err := MergeAllHeaders(Auth{}, Headers{
		Literal: map[string]string{"X-Org": "acme", "X-Project": "demo"},
	}, "")
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got["X-Org"] != "acme" || got["X-Project"] != "demo" || len(got) != 2 {
		t.Errorf("got=%v, want X-Org=acme + X-Project=demo (2 entries)", got)
	}
}

func TestMergeAllHeaders_EnvResolvesValues(t *testing.T) {
	t.Setenv("MY_TOK", "secret123")
	got, err := MergeAllHeaders(Auth{}, Headers{
		Env: map[string]string{"X-Token": "MY_TOK"},
	}, "")
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got["X-Token"] != "secret123" {
		t.Errorf("X-Token=%q, want %q", got["X-Token"], "secret123")
	}
}

func TestMergeAllHeaders_EnvOverridesLiteralOnCollision(t *testing.T) {
	t.Setenv("OVERRIDE_VAL", "from-env")
	got, err := MergeAllHeaders(Auth{}, Headers{
		Literal: map[string]string{"X-Header": "from-yaml"},
		Env:     map[string]string{"X-Header": "OVERRIDE_VAL"},
	}, "")
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got["X-Header"] != "from-env" {
		t.Errorf("collision: got %q, want %q (Env must override Literal)", got["X-Header"], "from-env")
	}
}

func TestMergeAllHeaders_EnvMissingErrors(t *testing.T) {
	t.Setenv("EMPTY_VAR", "")
	_, err := MergeAllHeaders(Auth{}, Headers{
		Env: map[string]string{"X-Missing": "EMPTY_VAR"},
	}, "")
	if err == nil {
		t.Fatal("got nil err, want non-nil for unset env")
	}
	if !strings.Contains(err.Error(), "EMPTY_VAR") || !strings.Contains(err.Error(), "X-Missing") {
		t.Errorf("err should mention env var and header name; got: %v", err)
	}
}

func TestMergeAllHeaders_AuthAndUserMerge(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc123")
	got, err := MergeAllHeaders(
		Auth{Type: "bearer", CredentialEnv: "MY_TOKEN"},
		Headers{Literal: map[string]string{"X-Org": "acme"}},
		"",
	)
	if err != nil {
		t.Fatalf("got err=%v, want nil", err)
	}
	if got["Authorization"] != "Bearer abc123" || got["X-Org"] != "acme" || len(got) != 2 {
		t.Errorf("got=%v, want Authorization+X-Org (2 entries)", got)
	}
}

func TestMergeAllHeaders_AuthCollidesWithUserHeaderErrors(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc123")
	_, err := MergeAllHeaders(
		Auth{Type: "bearer", CredentialEnv: "MY_TOKEN"},
		Headers{Literal: map[string]string{"Authorization": "Bearer manual"}},
		"",
	)
	if err == nil {
		t.Fatal("got nil err, want non-nil for header collision")
	}
	if !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("err should mention the colliding header name; got: %v", err)
	}
}

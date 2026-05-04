// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package auth holds shared, transport-agnostic data types and helpers
// for describing how AX authenticates outbound calls to remote agents
// and what extra headers to attach.
package auth

import (
	"errors"
	"fmt"
	"maps"
	"os"
)

// Auth describes how AX authenticates to a remote agent. Two well-known
// schemes are supported, both sourcing their credential value from a
// single environment variable named in CredentialEnv:
//   - "api_key": the credential is injected into a single HTTP header.
//     For A2A consumers, the header name is determined at runtime from
//     the agent's APIKeySecurityScheme on the AgentCard.
//   - "bearer": the credential is injected as
//     "Authorization: Bearer <value>".
type Auth struct {
	Type          string `yaml:"type"`           // "api_key" or "bearer"
	CredentialEnv string `yaml:"credential_env"` // env var holding credential value
}

// Headers is a reusable bundle of HTTP-style header values, with both
// literal and env-sourced sources.
type Headers struct {
	Literal map[string]string `yaml:"literal,omitempty"` // literal values (NOT secret-safe)
	Env     map[string]string `yaml:"env,omitempty"`     // header name -> env var name (secret-safe)
}

// resolveAuthHeader translates an Auth into a single (header name, value)
// tuple to inject.
func resolveAuthHeader(a Auth, apiKeyHeader string) (name, value string, err error) {
	if a.CredentialEnv == "" {
		return "", "", errors.New(`auth: credential_env is required`)
	}
	value = os.Getenv(a.CredentialEnv)
	if value == "" {
		return "", "", fmt.Errorf(`auth: env var %q is empty or unset`, a.CredentialEnv)
	}
	switch a.Type {
	case "bearer":
		return "Authorization", "Bearer " + value, nil
	case "api_key":
		if apiKeyHeader == "" {
			return "", "", errors.New(`auth.api_key: no header name provided by the transport`)
		}
		return apiKeyHeader, value, nil
	default:
		return "", "", fmt.Errorf(`auth.type: unsupported %q (want "api_key" or "bearer")`, a.Type)
	}
}

// MergeAllHeaders flattens user-supplied Headers and the auth-resolved
// header into a single map[string]string applied to every outgoing
// request. Returns (nil, nil) when no headers are produced.
func MergeAllHeaders(a Auth, h Headers, apiKeyHeader string) (map[string]string, error) {
	out := make(map[string]string, len(h.Literal)+len(h.Env)+1)
	maps.Copy(out, h.Literal)
	for k, envVar := range h.Env {
		v := os.Getenv(envVar)
		if v == "" {
			return nil, fmt.Errorf("headers.env[%q]: env var %q is empty or unset", k, envVar)
		}
		out[k] = v
	}
	if a.Type != "" {
		name, value, err := resolveAuthHeader(a, apiKeyHeader)
		if err != nil {
			return nil, err
		}
		if _, exists := out[name]; exists {
			return nil, fmt.Errorf(
				"auth resolves to header %q which is also set via headers; remove one source",
				name)
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

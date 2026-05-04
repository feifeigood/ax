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

package a2abridge

import (
	"context"
	"errors"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/google/ax/internal/auth"
)

// apiKeyHeaderFromCard looks up the api_key header name from an
// AgentCard's APIKeySecurityScheme.
// Returns ("", error) when the card declares no APIKeySecurityScheme
// with Location=header.
func apiKeyHeaderFromCard(card *a2a.AgentCard) (string, error) {
	if card != nil {
		for _, scheme := range card.SecuritySchemes {
			apiKey, ok := scheme.(a2a.APIKeySecurityScheme)
			if !ok {
				continue
			}
			if apiKey.Location == a2a.APIKeySecuritySchemeLocationHeader && apiKey.Name != "" {
				return apiKey.Name, nil
			}
		}
	}
	return "", errors.New(
		`auth.api_key: AgentCard does not declare an APIKeySecurityScheme ` +
			`with Location=header; specify the API key header via headers.env instead`)
}

// interceptor implements a2aclient.CallInterceptor and writes a fixed map
// of headers directly to req.ServiceParams on every outgoing request. The
// transport (HTTP / JSON-RPC / REST / gRPC) serialises ServiceParams into
// headers per its protocol binding.
type interceptor struct {
	headers map[string]string
}

// NewInterceptor builds an a2aclient.CallInterceptor that injects the
// merged auth + user headers on every outgoing call. Returns (nil, nil)
// when no headers are configured.
func NewInterceptor(card *a2a.AgentCard, a auth.Auth, h auth.Headers) (a2aclient.CallInterceptor, error) {
	apiKeyHeader := ""
	if a.Type == "api_key" {
		name, err := apiKeyHeaderFromCard(card)
		if err != nil {
			return nil, err
		}
		apiKeyHeader = name
	}
	headers, err := auth.MergeAllHeaders(a, h, apiKeyHeader)
	if err != nil {
		return nil, err
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return &interceptor{headers: headers}, nil
}

// Before implements a2aclient.CallInterceptor.
func (i *interceptor) Before(ctx context.Context, req *a2aclient.Request) (context.Context, any, error) {
	if len(i.headers) == 0 {
		return ctx, nil, nil
	}
	if req.ServiceParams == nil {
		req.ServiceParams = make(a2aclient.ServiceParams)
	}
	for k, v := range i.headers {
		// Set semantics (overwrite existing entry for the same key).
		req.ServiceParams[k] = []string{v}
	}
	return ctx, nil, nil
}

// After implements a2aclient.CallInterceptor.
func (i *interceptor) After(_ context.Context, _ *a2aclient.Response) error {
	return nil
}

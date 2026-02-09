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
	"testing"
)

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		wantErr   bool
	}{
		{
			name:      "valid lowercase",
			sessionID: "session123",
			wantErr:   false,
		},
		{
			name:      "valid mixed",
			sessionID: "Session-ID_123",
			wantErr:   false,
		},
		{
			name:      "valid simple",
			sessionID: "Session-ID",
			wantErr:   false,
		},
		{
			name:      "valid underscore",
			sessionID: "session_id",
			wantErr:   false,
		},
		{
			name:      "invalid space",
			sessionID: "session id",
			wantErr:   true,
		},
		{
			name:      "invalid char",
			sessionID: "session!",
			wantErr:   true,
		},
		{
			name:      "empty",
			sessionID: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateID(tt.sessionID)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSessionID(%q) error = %v, wantErr %v", tt.sessionID, err, tt.wantErr)
			}
		})
	}
}

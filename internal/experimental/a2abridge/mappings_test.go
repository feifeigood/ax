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
	"testing"

	"github.com/google/ax/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestMimeTableCoverage verifies that every non-UNSPECIFIED MIME enum
// value declared in proto/content.proto has a corresponding wire-string
// entry in mime_map.go's forward table.
func TestMimeTableCoverage(t *testing.T) {
	t.Run("ImageContent_MimeType", func(t *testing.T) {
		assertMimeCoverage(t, proto.ImageContent_MimeType(0).Descriptor(), func(n int32) bool {
			_, ok := imageMimeToString[proto.ImageContent_MimeType(n)]
			return ok
		})
	})
	t.Run("AudioContent_MimeType", func(t *testing.T) {
		assertMimeCoverage(t, proto.AudioContent_MimeType(0).Descriptor(), func(n int32) bool {
			_, ok := audioMimeToString[proto.AudioContent_MimeType(n)]
			return ok
		})
	})
	t.Run("VideoContent_MimeType", func(t *testing.T) {
		assertMimeCoverage(t, proto.VideoContent_MimeType(0).Descriptor(), func(n int32) bool {
			_, ok := videoMimeToString[proto.VideoContent_MimeType(n)]
			return ok
		})
	})
	t.Run("DocumentContent_MimeType", func(t *testing.T) {
		assertMimeCoverage(t, proto.DocumentContent_MimeType(0).Descriptor(), func(n int32) bool {
			_, ok := documentMimeToString[proto.DocumentContent_MimeType(n)]
			return ok
		})
	})
}

func assertMimeCoverage(t *testing.T, desc protoreflect.EnumDescriptor, has func(int32) bool) {
	t.Helper()
	var missing []string
	values := desc.Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if int32(v.Number()) == 0 {
			continue // UNSPECIFIED; intentionally not mapped
		}
		if !has(int32(v.Number())) {
			missing = append(missing, string(v.Name()))
		}
	}
	if len(missing) > 0 {
		t.Errorf("mime_map missing wire-string entries for: %v (add the wire MIME string to the corresponding forward map in mime_map.go)", missing)
	}
}

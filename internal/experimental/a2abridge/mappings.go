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
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/ax/proto"
)

// ----- enum <-> wire-string MIME mapping tables -----
//
// content.proto encodes canonical MIME wire strings (e.g. "image/png")
// as annotations on each enum value. We maintain the forward (string -> enum)
// tables here and derive the inverse (enum -> string) maps via invertMap.
//
// These tables are the single source of truth for the bridge's typed-
// content conversion in both directions. A test guards that they stay in sync
// with the content.proto annotations.

var imageMimeFromString = map[string]proto.ImageContent_MimeType{
	"image/png":  proto.ImageContent_TYPE_PNG,
	"image/jpeg": proto.ImageContent_TYPE_JPEG,
	"image/webp": proto.ImageContent_TYPE_WEBP,
	"image/heic": proto.ImageContent_TYPE_HEIC,
	"image/heif": proto.ImageContent_TYPE_HEIF,
	"image/gif":  proto.ImageContent_TYPE_GIF,
	"image/bmp":  proto.ImageContent_TYPE_BMP,
	"image/tiff": proto.ImageContent_TYPE_TIFF,
}

var audioMimeFromString = map[string]proto.AudioContent_MimeType{
	"audio/wav":   proto.AudioContent_TYPE_WAV,
	"audio/mp3":   proto.AudioContent_TYPE_MP3,
	"audio/aiff":  proto.AudioContent_TYPE_AIFF,
	"audio/aac":   proto.AudioContent_TYPE_AAC,
	"audio/ogg":   proto.AudioContent_TYPE_OGG,
	"audio/flac":  proto.AudioContent_TYPE_FLAC,
	"audio/mpeg":  proto.AudioContent_TYPE_MPEG,
	"audio/m4a":   proto.AudioContent_TYPE_M4A,
	"audio/l16":   proto.AudioContent_TYPE_L16,
	"audio/s16le": proto.AudioContent_TYPE_S16LE,
	"audio/opus":  proto.AudioContent_TYPE_OPUS,
	"audio/alaw":  proto.AudioContent_TYPE_ALAW,
	"audio/mulaw": proto.AudioContent_TYPE_MULAW,
}

var videoMimeFromString = map[string]proto.VideoContent_MimeType{
	"video/mp4":   proto.VideoContent_TYPE_MP4,
	"video/mpeg":  proto.VideoContent_TYPE_MPEG,
	"video/mpg":   proto.VideoContent_TYPE_MPG,
	"video/mov":   proto.VideoContent_TYPE_MOV,
	"video/avi":   proto.VideoContent_TYPE_AVI,
	"video/x-flv": proto.VideoContent_TYPE_X_FLV,
	"video/webm":  proto.VideoContent_TYPE_WEBM,
	"video/wmv":   proto.VideoContent_TYPE_WMV,
	"video/3gpp":  proto.VideoContent_TYPE_3GPP,
}

var documentMimeFromString = map[string]proto.DocumentContent_MimeType{
	"application/pdf":  proto.DocumentContent_TYPE_PDF,
	"application/json": proto.DocumentContent_TYPE_JSON,
	"text/x-python":    proto.DocumentContent_TYPE_PYTHON,
}

var (
	imageMimeToString    = invertMap(imageMimeFromString)
	audioMimeToString    = invertMap(audioMimeFromString)
	videoMimeToString    = invertMap(videoMimeFromString)
	documentMimeToString = invertMap(documentMimeFromString)
)

// ----- role mapping -----
//
// stringToA2ARole is the strict source-of-truth map for role translation
// between AX (string) and A2A (typed enum). The inverse (a2aRoleToString)
// is derived via invertMap so adding an entry here automatically
// propagates to both directions.
var stringToA2ARole = map[string]a2a.MessageRole{
	"unspecified": a2a.MessageRoleUnspecified,
	"user":        a2a.MessageRoleUser,
	"agent":       a2a.MessageRoleAgent,
}

var a2aRoleToString = invertMap(stringToA2ARole)

// invertMap returns a new map with keys and values swapped. If two keys
// map to the same value, last write wins per Go map iteration order.
func invertMap[K, V comparable](in map[K]V) map[V]K {
	out := make(map[V]K, len(in))
	for k, v := range in {
		out[v] = k
	}
	return out
}

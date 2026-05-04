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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/ax/proto"
)

// ----- AX -> A2A direction -----

// MessagesToA2AParts converts AX message history into a flat list of A2A
// Parts. Internal-only AX content are skipped.
func MessagesToA2AParts(msgs []*proto.Message) []*a2a.Part {
	out := make([]*a2a.Part, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil || msg.Content == nil {
			continue
		}
		if isStateMarkerMessage(msg) {
			continue
		}
		if p := contentToA2APart(msg.Content); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// LatestUserInputParts returns the most recent user-role input as a single
// A2A Part - i.e. what the AX user just typed. The walk goes backward
// from the end of msgs and returns as soon as it finds a user-role
// message with usable content (text/file/data).
func LatestUserInputParts(msgs []*proto.Message) []*a2a.Part {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg == nil || msg.Role != "user" || msg.Content == nil {
			continue
		}
		if isStateMarkerMessage(msg) {
			continue
		}
		switch msg.Content.Type.(type) {
		case *proto.Content_Confirmation,
			*proto.Content_ToolCall,
			*proto.Content_ToolResult,
			*proto.Content_Thought:
			continue
		}
		if p := contentToA2APart(msg.Content); p != nil {
			return []*a2a.Part{p}
		}
	}
	return nil
}

// contentToA2APart converts a single AX Content variant into an A2A Part.
func contentToA2APart(content *proto.Content) *a2a.Part {
	if content == nil {
		return nil
	}
	switch t := content.Type.(type) {
	case *proto.Content_Text:
		if t.Text == nil || t.Text.Text == "" {
			return nil
		}
		return a2a.NewTextPart(t.Text.Text)
	case *proto.Content_Image:
		return imageContentToPart(t.Image)
	case *proto.Content_Audio:
		return audioContentToPart(t.Audio)
	case *proto.Content_Video:
		return videoContentToPart(t.Video)
	case *proto.Content_Document:
		return documentContentToPart(t.Document)
	}
	return nil
}

// rawOrURLPart builds an A2A Part from inline bytes (Raw variant) or a URI
// (URL variant), whichever is non-empty. Returns nil if both are empty.
func rawOrURLPart(data []byte, uri, mediaType string) *a2a.Part {
	if len(data) > 0 {
		return &a2a.Part{Content: a2a.Raw(data), MediaType: mediaType}
	}
	if uri != "" {
		return &a2a.Part{Content: a2a.URL(uri), MediaType: mediaType}
	}
	return nil
}

// mediaTypeOr returns table[enum] if present, otherwise fallback. Used by
// the typed-content converters to resolve the wire-mime string with a
// type-specific generic ("image/*", "audio/*", etc.) when the AX enum
// value isn't in our mapping table.
func mediaTypeOr[E comparable](table map[E]string, enum E, fallback string) string {
	if s := table[enum]; s != "" {
		return s
	}
	return fallback
}

func imageContentToPart(c *proto.ImageContent) *a2a.Part {
	if c == nil {
		return nil
	}
	return rawOrURLPart(c.GetData(), c.GetUri(), mediaTypeOr(imageMimeToString, c.MimeType, "image/*"))
}

func audioContentToPart(c *proto.AudioContent) *a2a.Part {
	if c == nil {
		return nil
	}
	return rawOrURLPart(c.GetData(), c.GetUri(), mediaTypeOr(audioMimeToString, c.MimeType, "audio/*"))
}

func videoContentToPart(c *proto.VideoContent) *a2a.Part {
	if c == nil {
		return nil
	}
	return rawOrURLPart(c.GetData(), c.GetUri(), mediaTypeOr(videoMimeToString, c.MimeType, "video/*"))
}

func documentContentToPart(c *proto.DocumentContent) *a2a.Part {
	if c == nil {
		return nil
	}
	// DocumentContent with TYPE_JSON is the round-trip representation of
	// an A2A DataPart - convert it back. Falls through to raw bytes if
	// the JSON is malformed.
	if c.MimeType == proto.DocumentContent_TYPE_JSON {
		if data := c.GetData(); len(data) > 0 {
			var v any
			if err := json.Unmarshal(data, &v); err == nil {
				return &a2a.Part{
					Content:   a2a.Data{Value: v},
					MediaType: "application/json",
				}
			}
		}
	}
	return rawOrURLPart(c.GetData(), c.GetUri(), mediaTypeOr(documentMimeToString, c.MimeType, "application/octet-stream"))
}

// ----- A2A -> AX direction -----

// a2aPartsToMessages converts a list of A2A Parts into AX Messages. Each
// Part becomes one Message with the supplied role.
func a2aPartsToMessages(parts []*a2a.Part, role string) []*proto.Message {
	out := make([]*proto.Message, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		content := a2aPartToContent(part)
		if content == nil {
			continue
		}
		out = append(out, &proto.Message{Role: role, Content: content})
	}
	return out
}

// a2aPartToContent classifies an A2A Part by its Content variant and
// MediaType and produces the appropriate AX Content. Returns nil if the part
// is empty or unrecognised.
func a2aPartToContent(part *a2a.Part) *proto.Content {
	if part == nil {
		return nil
	}
	switch c := part.Content.(type) {
	case a2a.Text:
		text := string(c)
		if text == "" {
			return nil
		}
		return &proto.Content{
			Type: &proto.Content_Text{
				Text: &proto.TextContent{Text: text},
			},
		}
	case a2a.Raw:
		return classifyByMediaType([]byte(c), "", part.MediaType)
	case a2a.URL:
		return classifyByMediaType(nil, string(c), part.MediaType)
	case a2a.Data:
		return dataValueToContent(c.Value)
	}
	return nil
}

// classifyByMediaType builds an AX Content from inline bytes (data) OR a
// URL reference (uri); pass exactly one. The wire MIME string selects
// the typed Content variant (Image/Audio/Video/Document); unrecognised
// MIME types land in DocumentContent with TYPE_UNSPECIFIED.
func classifyByMediaType(data []byte, uri, mediaType string) *proto.Content {
	if e, ok := imageMimeFromString[mediaType]; ok {
		return newImageContent(e, data, uri)
	}
	if e, ok := audioMimeFromString[mediaType]; ok {
		return newAudioContent(e, data, uri)
	}
	if e, ok := videoMimeFromString[mediaType]; ok {
		return newVideoContent(e, data, uri)
	}
	if e, ok := documentMimeFromString[mediaType]; ok {
		return newDocumentContent(e, data, uri)
	}
	return newDocumentContent(proto.DocumentContent_TYPE_UNSPECIFIED, data, uri)
}

// newImageContent constructs an Image-typed proto.Content from inline
// bytes or a URI (whichever is non-empty); pass exactly one.
func newImageContent(mime proto.ImageContent_MimeType, data []byte, uri string) *proto.Content {
	img := &proto.ImageContent{MimeType: mime}
	if len(data) > 0 {
		img.DataOrUri = &proto.ImageContent_Data{Data: data}
	} else if uri != "" {
		img.DataOrUri = &proto.ImageContent_Uri{Uri: uri}
	}
	return &proto.Content{Type: &proto.Content_Image{Image: img}}
}

// newAudioContent constructs an Audio-typed proto.Content from inline
// bytes or a URI (whichever is non-empty); pass exactly one.
func newAudioContent(mime proto.AudioContent_MimeType, data []byte, uri string) *proto.Content {
	aud := &proto.AudioContent{MimeType: mime}
	if len(data) > 0 {
		aud.DataOrUri = &proto.AudioContent_Data{Data: data}
	} else if uri != "" {
		aud.DataOrUri = &proto.AudioContent_Uri{Uri: uri}
	}
	return &proto.Content{Type: &proto.Content_Audio{Audio: aud}}
}

// newVideoContent constructs a Video-typed proto.Content from inline
// bytes or a URI (whichever is non-empty); pass exactly one.
func newVideoContent(mime proto.VideoContent_MimeType, data []byte, uri string) *proto.Content {
	vid := &proto.VideoContent{MimeType: mime}
	if len(data) > 0 {
		vid.DataOrUri = &proto.VideoContent_Data{Data: data}
	} else if uri != "" {
		vid.DataOrUri = &proto.VideoContent_Uri{Uri: uri}
	}
	return &proto.Content{Type: &proto.Content_Video{Video: vid}}
}

// newDocumentContent constructs a Document-typed proto.Content from
// inline bytes or a URI (whichever is non-empty); pass exactly one.
func newDocumentContent(mime proto.DocumentContent_MimeType, data []byte, uri string) *proto.Content {
	doc := &proto.DocumentContent{MimeType: mime}
	if len(data) > 0 {
		doc.DataOrUri = &proto.DocumentContent_Data{Data: data}
	} else if uri != "" {
		doc.DataOrUri = &proto.DocumentContent_Uri{Uri: uri}
	}
	return &proto.Content{Type: &proto.Content_Document{Document: doc}}
}

// dataValueToContent serialises arbitrary structured data into JSON
// bytes stored as a DocumentContent with mime_type=TYPE_JSON.
func dataValueToContent(value any) *proto.Content {
	bytes, err := json.Marshal(value)
	if err != nil {
		// Fall back to a string representation so we never lose the data
		// entirely.
		bytes = fmt.Appendf(nil, "%v", value)
	}
	return &proto.Content{Type: &proto.Content_Document{
		Document: &proto.DocumentContent{
			MimeType:  proto.DocumentContent_TYPE_JSON,
			DataOrUri: &proto.DocumentContent_Data{Data: bytes},
		},
	}}
}

// a2aArtifactToMessages converts an A2A Artifact into a list of AX Messages
// by flattening its Parts.
func a2aArtifactToMessages(artifact *a2a.Artifact, role string) []*proto.Message {
	if artifact == nil {
		return nil
	}
	out := make([]*proto.Message, 0, len(artifact.Parts))
	for _, part := range artifact.Parts {
		content := a2aPartToContent(part)
		if content == nil {
			continue
		}
		out = append(out, &proto.Message{Role: role, Content: content})
	}
	return out
}

// a2aRoleToRole maps A2A's MessageRole back to AX's role string.
func a2aRoleToRole(role a2a.MessageRole) string {
	if s, ok := a2aRoleToString[role]; ok {
		return s
	}
	return "unspecified"
}

// ----- card-derived metadata -----

// AgentMetadataFromCard composes the AX-side (Name, Description) for a
// registered A2A agent from its AgentCard plus optional config-supplied
// overrides.
func AgentMetadataFromCard(card *a2a.AgentCard, cfgName, cfgDescription string) (name, description string) {
	name = cfgName
	description = cfgDescription
	if card != nil {
		if name == "" {
			name = card.Name
		}
		if description == "" {
			description = card.Description
		}
		description = enrichDescriptionWithSkills(description, card)
	}
	return name, description
}

// enrichDescriptionWithSkills appends a structured summary of the agent's
// Skills (name, description, examples, tags) to the base description.
func enrichDescriptionWithSkills(base string, card *a2a.AgentCard) string {
	if card == nil || len(card.Skills) == 0 {
		return base
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(base, " \n"))
	if base != "" && !strings.HasSuffix(strings.TrimRight(base, " \n"), ".") {
		b.WriteString(".")
	}
	b.WriteString("\n\nSkills:\n")
	for _, s := range card.Skills {
		fmt.Fprintf(&b, "- %s: %s", s.Name, s.Description)
		if len(s.Tags) > 0 {
			fmt.Fprintf(&b, " (tags: %s)", strings.Join(s.Tags, ", "))
		}
		b.WriteString("\n")
		if len(s.Examples) > 0 {
			b.WriteString("  Examples: ")
			for i, ex := range s.Examples {
				if i > 0 {
					b.WriteString("; ")
				}
				fmt.Fprintf(&b, "%q", ex)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

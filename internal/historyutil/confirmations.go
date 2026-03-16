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

package historyutil

import "github.com/google/ax/proto"

// WaitsForConfirmation returns true if the last message in the history
// is a confirmation question waiting for user input.
func WaitsForConfirmation(history []*proto.Content) bool {
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	if last.GetConfirmation() != nil && last.GetConfirmation().Question != "" {
		return true
	}
	return false
}

// HasConfirmationAnswer returns true if the last message in the history
// is a confirmation question waiting for user input.
func HasConfirmationAnswer(history []*proto.Content) (approved bool, conf *proto.ConfirmationContent) {
	if len(history) == 0 {
		return false, nil
	}
	last := history[len(history)-1]
	if last.GetConfirmation() == nil {
		return false, nil
	}
	if last.GetConfirmation().GetApproval() != nil {
		conf = last.GetConfirmation()
		approved = true
	}
	if last.GetConfirmation().GetDecline() != nil {
		conf = last.GetConfirmation()
		approved = false
	}
	return approved, conf
}

func WaitsForUser(history []*proto.Content) bool {
	if len(history) == 0 {
		return false
	}
	if WaitsForConfirmation(history) {
		return true
	}

	last := history[len(history)-1]
	if last.GetConfirmation() != nil && last.GetConfirmation().GetDecline() != nil {
		return true
	}
	return false
}

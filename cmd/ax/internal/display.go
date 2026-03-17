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

package internal

import (
	"fmt"
	"sync/atomic"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

const (
	boxWidth = 100
)

var (
	purple  = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#7571F9"}
	comment = lipgloss.AdaptiveColor{Dark: "#6d6d6d"}
)

type Display struct {
	id string

	userStyle       lipgloss.Style
	checkpointStyle lipgloss.Style
	idStyle         lipgloss.Style

	loadingVisible atomic.Bool
	loadingStopCh  chan bool
}

func NewDisplay(id string) *Display {
	return &Display{
		id: id,
		userStyle: lipgloss.NewStyle().
			Foreground(purple),
		checkpointStyle: lipgloss.NewStyle().
			Foreground(comment),
		idStyle: lipgloss.NewStyle().
			Foreground(comment),
		loadingStopCh: make(chan bool),
	}
}

// DisplayInput displays the user input.
func (d *Display) DisplayInput(text string) {
	fmt.Printf("%s %s\n",
		d.userStyle.Render("⏺"),
		text,
	)
	fmt.Println()
}

// DisplayOutput displays an output fragment.
func (d *Display) DisplayOutput(text string) {
	fmt.Print(text)
}

// Finish completes the streaming output and shows checkpoint if provided
func (d *Display) FinishOutput(checkpointID string) {
	fmt.Print("\n")

	// Show checkpoint box if checkpoint ID exists
	if checkpointID != "" {
		fmt.Println(d.checkpointStyle.Render("Checkpoint: " + checkpointID))
	}
	fmt.Println()
}

func (d *Display) DisplayHeader() {
	fmt.Println(d.idStyle.Render("ID: " + d.id))
	fmt.Println()
}

// PromptForApproval shows an accept/reject dialog
// Returns true if accepted, false if rejected, and an error if cancelled (Ctrl+C)
func (d *Display) PromptForApproval(question string) (bool, error) {
	var accepted bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(question).
				Affirmative("Accept").
				Negative("Reject").
				Value(&accepted),
		),
	).WithWidth(boxWidth)

	if err := form.Run(); err != nil {
		return false, err
	}
	return accepted, nil
}

// PromptForInput shows the input box and returns the user input
// Returns the input string and an error if the user cancelled (Ctrl+C)
func (d *Display) PromptForInput() (string, error) {
	var userInput string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Placeholder("Enter prompt...").
				Value(&userInput),
		),
	).WithWidth(boxWidth)

	if err := form.Run(); err != nil {
		return "", err
	}
	return userInput, nil
}

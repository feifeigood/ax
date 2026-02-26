package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/google/gar/internal/skills"
	"google.golang.org/genai"
)

func main() {
	ctx := context.Background()

	// 1. Get the absolute path of the current skills directory
	_, filename, _, _ := runtime.Caller(0)
	skillsDir := filepath.Dir(filename) // Pointing to examples/skills/

	fmt.Println("=== 1. Discovering Skills ===")
	found, err := skills.Discover(skillsDir)
	if err != nil {
		log.Fatalf("Failed to discover skills: %v", err)
	}
	for _, s := range found {
		fmt.Printf(" - Found Skill: %s\n", s.Name)
	}

	fmt.Println("\n=== 2. Initializing AI Executor ===")
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY environment variable is required")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		log.Fatalf("Failed to create Gemini client: %v", err)
	}

	executor, err := skills.NewExecutor(client, "gemini-3-flash-preview", skillsDir)
	if err != nil {
		log.Fatalf("Failed to create executor: %v", err)
	}

	executor.OnUpdate(func(ev skills.UpdateEvent) {
		switch ev.Kind {
		case skills.UpdateSkillActivated:
			fmt.Printf("\n[System Notification: The LLM activated skill '%s']\n", ev.Skill)
		case skills.UpdateScriptRunning:
			fmt.Printf("\n[System Notification: The LLM is running script '%s' from skill '%s']\n", ev.Script, ev.Skill)
		}
	})

	prompt := "Please give me an emoji for: 'magic'"
	fmt.Printf("\n=== 3. Running Agentic Loop with Prompt ===\nUser: %s\n\n", prompt)

	response, err := executor.Run(ctx, prompt)
	if err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	fmt.Printf("\n=== Final Response ===\n%s\n", response)
}

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/richardartoul/swarmd/pkg/agent"
)

func main() {
	root, err := os.MkdirTemp("", "swarmd-minimal-agent")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	runtime, err := agent.New(agent.Config{
		Root: root,
		Driver: agent.DriverFunc(func(_ context.Context, req agent.Request) (agent.Decision, error) {
			switch req.Step {
			case 1:
				return agent.Decision{
					Tool: &agent.ToolAction{
						Name:  agent.ToolNameRunShell,
						Kind:  agent.ToolKindFunction,
						Input: `{"command":"echo hello from the sandbox"}`,
					},
				}, nil
			default:
				return agent.Decision{
					Finish: &agent.FinishAction{Value: "done"},
				}, nil
			}
		}),
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			panic(err)
		}
	}()

	result, err := runtime.HandleTrigger(context.Background(), agent.Trigger{Kind: "example"})
	if err != nil {
		panic(err)
	}
	if len(result.Steps) == 0 {
		panic("expected at least one step")
	}

	fmt.Printf("status: %s\n", result.Status)
	fmt.Printf("stdout: %s\n", strings.TrimSpace(result.Steps[0].Stdout))
	fmt.Printf("value: %v\n", result.Value)
}

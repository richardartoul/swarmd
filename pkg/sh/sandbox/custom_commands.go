// See LICENSE for licensing information

package sandbox

import (
	"context"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

// CommandInfo describes a sandbox command exposed to the shell agent.
type CommandInfo struct {
	Name            string
	Usage           string
	Description     string
	RequiresNetwork bool
}

// CustomCommand defines a host-provided sandbox command implementation.
type CustomCommand struct {
	Info CommandInfo
	Run  func(ctx context.Context, args []string) error
}

func customCommandInfos(commands []CustomCommand) []CommandInfo {
	if len(commands) == 0 {
		return nil
	}
	infos := make([]CommandInfo, 0, len(commands))
	for _, command := range commands {
		infos = append(infos, command.Info)
	}
	return infos
}

func customCommandExecHandler(commands []CustomCommand) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	byName := make(map[string]CustomCommand, len(commands))
	for _, command := range commands {
		byName[command.Info.Name] = command
	}
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			command, ok := byName[args[0]]
			if !ok {
				return next(ctx, args)
			}
			return command.Run(ctx, args[1:])
		}
	}
}

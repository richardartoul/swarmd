package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

func runTUICommand(ctx context.Context, args []string, streams commandIO) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(streams.stderr)

	dbPath := envOr("SWARMD_SERVER_DB", defaultSQLitePath)
	fs.StringVar(&dbPath, "db", dbPath, "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureNoExtraArgs(fs, "tui"); err != nil {
		return err
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("tui requires an interactive terminal")
	}

	store, resolvedDBPath, err := openReadOnlyStore(ctx, fs, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	model, err := newTUIModel(ctx, store, resolvedDBPath)
	if err != nil {
		return err
	}

	program := tea.NewProgram(
		model,
		tea.WithAltScreen(),
		tea.WithContext(ctx),
		tea.WithMouseCellMotion(),
	)
	_, err = program.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
)

type serverConfig struct {
	dataDir             string
	dbPath              string
	configRoot          string
	rootBase            string
	openAIAPIKey        string
	anthropicAPIKey     string
	slackUserToken      string
	runtimePollInterval time.Duration
	schedulerPoll       time.Duration
	schedulerBatchSize  int
	liveOutput          bool
}

func runServer(ctx context.Context, args []string, streams commandIO) error {
	cfg, err := parseServerConfig(args, streams.stderr)
	if err != nil {
		return err
	}
	specs, err := server.LoadAgentSpecs(cfg.configRoot)
	if err != nil {
		return err
	}
	if err := validateServerAPIKeys(specs, cfg); err != nil {
		return err
	}
	if err := server.ValidateReferencedToolEnv(specs, serverConfigEnvLookup(cfg)); err != nil {
		return err
	}
	if err := server.ValidateReferencedAgentConfigEnv(specs, serverConfigEnvLookup(cfg)); err != nil {
		return err
	}
	if err := ensureServerDataPaths(cfg); err != nil {
		return err
	}

	envLookup := serverConfigEnvLookup(cfg)

	store, err := cpstore.Open(ctx, cfg.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	summary, err := server.SyncSpecsFromConfigRoot(ctx, store, cfg.configRoot, cfg.rootBase)
	if err != nil {
		return err
	}

	var liveStdout, liveStderr io.Writer
	if cfg.liveOutput {
		liveStdout = streams.stdout
		liveStderr = streams.stderr
	}

	env := server.Environment{
		Runtime: &server.RuntimeManager{
			Store: store,
			DriverFactory: server.MultiProviderWorkerDriverFactory{
				OpenAIAPIKey:    cfg.openAIAPIKey,
				AnthropicAPIKey: cfg.anthropicAPIKey,
			},
			PollInterval: cfg.runtimePollInterval,
			Stdout:       liveStdout,
			Stderr:       liveStderr,
			Logger:       server.NewRuntimeLogger(streams.stdout, streams.stderr),
			EnvLookup:    envLookup,
		},
		Scheduler: &server.Scheduler{
			Store:        store,
			PollInterval: cfg.schedulerPoll,
			BatchSize:    cfg.schedulerBatchSize,
		},
	}

	fmt.Fprintf(
		streams.stdout,
		"server> sqlite=%s config_root=%s data_dir=%s root_base=%s\n",
		cfg.dbPath,
		cfg.configRoot,
		cfg.dataDir,
		cfg.rootBase,
	)
	fmt.Fprintf(
		streams.stdout,
		"sync> namespaces(created=%d updated=%d) agents(created=%d updated=%d deleted=%d) schedules(created=%d deleted=%d)\n",
		summary.NamespacesCreated,
		summary.NamespacesUpdated,
		summary.AgentsCreated,
		summary.AgentsUpdated,
		summary.AgentsDeleted,
		summary.SchedulesCreated,
		summary.SchedulesDeleted,
	)
	if err := env.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func parseServerConfig(args []string, stderr io.Writer) (serverConfig, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)

	dataDirDefault := envOr("SWARMD_SERVER_DATA_DIR", defaultDataDir)
	cfg := serverConfig{}
	fs.StringVar(&cfg.dataDir, "data-dir", dataDirDefault, "Base directory for server state and default agent roots")
	fs.StringVar(&cfg.dbPath, "db", envOr("SWARMD_SERVER_DB", defaultSQLitePathForDataDir(dataDirDefault)), "SQLite database path")
	fs.StringVar(&cfg.configRoot, "config-root", envOr("SWARMD_SERVER_CONFIG_ROOT", defaultConfigRoot), "Config root containing namespaces/<namespace>/agents/*.yaml")
	fs.StringVar(&cfg.rootBase, "root-base", envOr("SWARMD_SERVER_ROOT_BASE", defaultRootBaseForDataDir(dataDirDefault)), "Default base directory for agent work roots")
	fs.StringVar(&cfg.openAIAPIKey, "api-key", strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), "OpenAI API key for worker agents")
	fs.StringVar(&cfg.anthropicAPIKey, "anthropic-api-key", strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")), "Anthropic API key for worker agents")
	fs.StringVar(&cfg.slackUserToken, "slack-user-token", strings.TrimSpace(os.Getenv("SLACK_USER_TOKEN")), "Slack user token for Slack-backed sandbox commands")
	fs.DurationVar(&cfg.runtimePollInterval, "runtime-poll-interval", 2*time.Second, "Poll interval for worker runtime reconciliation")
	fs.DurationVar(&cfg.schedulerPoll, "scheduler-poll-interval", time.Second, "Poll interval for firing due schedules")
	fs.IntVar(&cfg.schedulerBatchSize, "scheduler-batch-size", 32, "Maximum number of schedules to fire per poll")
	fs.BoolVar(&cfg.liveOutput, "live-output", false, "Mirror live worker stdout/stderr to this process")

	if err := fs.Parse(args); err != nil {
		return serverConfig{}, err
	}
	if !flagWasSet(fs, "db") && strings.TrimSpace(os.Getenv("SWARMD_SERVER_DB")) == "" {
		cfg.dbPath = defaultSQLitePathForDataDir(cfg.dataDir)
	}
	if !flagWasSet(fs, "root-base") && strings.TrimSpace(os.Getenv("SWARMD_SERVER_ROOT_BASE")) == "" {
		cfg.rootBase = defaultRootBaseForDataDir(cfg.dataDir)
	}
	cfg.dataDir = strings.TrimSpace(cfg.dataDir)
	cfg.dbPath = strings.TrimSpace(cfg.dbPath)
	cfg.configRoot = strings.TrimSpace(cfg.configRoot)
	cfg.rootBase = strings.TrimSpace(cfg.rootBase)
	cfg.openAIAPIKey = strings.TrimSpace(cfg.openAIAPIKey)
	cfg.anthropicAPIKey = strings.TrimSpace(cfg.anthropicAPIKey)
	cfg.slackUserToken = strings.TrimSpace(cfg.slackUserToken)
	if cfg.dataDir == "" {
		return serverConfig{}, fmt.Errorf("server requires -data-dir or SWARMD_SERVER_DATA_DIR")
	}
	if cfg.dbPath == "" {
		return serverConfig{}, fmt.Errorf("server requires -db or SWARMD_SERVER_DB")
	}
	if cfg.configRoot == "" {
		return serverConfig{}, fmt.Errorf("server requires -config-root or SWARMD_SERVER_CONFIG_ROOT")
	}
	return cfg, nil
}

func validateServerAPIKeys(specs []server.AgentSpec, cfg serverConfig) error {
	var missing []string
	requiresOpenAI := false
	requiresAnthropic := false
	for _, spec := range specs {
		switch strings.TrimSpace(spec.Model.Provider) {
		case "", "openai":
			requiresOpenAI = true
		case "anthropic":
			requiresAnthropic = true
		}
	}
	if requiresOpenAI && strings.TrimSpace(cfg.openAIAPIKey) == "" {
		missing = append(missing, "server requires -api-key or OPENAI_API_KEY for openai agents")
	}
	if requiresAnthropic && strings.TrimSpace(cfg.anthropicAPIKey) == "" {
		missing = append(missing, "server requires -anthropic-api-key or ANTHROPIC_API_KEY for anthropic agents")
	}
	if len(missing) > 0 {
		return errors.New(strings.Join(missing, "; "))
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func serverConfigEnvLookup(cfg serverConfig) func(string) string {
	overrides := map[string]string{
		"SLACK_USER_TOKEN": strings.TrimSpace(cfg.slackUserToken),
	}
	return func(key string) string {
		if value, ok := overrides[key]; ok {
			return value
		}
		return strings.TrimSpace(os.Getenv(key))
	}
}

func defaultSQLitePathForDataDir(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), defaultSQLiteFilename)
}

func defaultRootBaseForDataDir(dataDir string) string {
	return filepath.Join(strings.TrimSpace(dataDir), defaultAgentRootsDir)
}

func sqliteLocalPath(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if !strings.HasPrefix(dsn, "file:") {
		return dsn, true
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if strings.EqualFold(parsed.Query().Get("mode"), "memory") {
		return "", false
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = strings.TrimSpace(parsed.Opaque)
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return "", false
	}
	return path, true
}

func sqliteLocalPathAbs(dsn string) (string, bool, error) {
	path, ok := sqliteLocalPath(dsn)
	if !ok {
		return "", false, nil
	}
	if filepath.IsAbs(path) {
		return path, true, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}
	return absPath, true, nil
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func ensureServerDataPaths(cfg serverConfig) error {
	if err := os.MkdirAll(cfg.dataDir, 0o755); err != nil {
		return fmt.Errorf("ensure data dir %q: %w", cfg.dataDir, err)
	}
	if strings.TrimSpace(cfg.rootBase) != "" {
		if err := os.MkdirAll(cfg.rootBase, 0o755); err != nil {
			return fmt.Errorf("ensure root base %q: %w", cfg.rootBase, err)
		}
	}
	if parentDir, ok := sqliteParentDir(cfg.dbPath); ok {
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			return fmt.Errorf("ensure sqlite parent %q: %w", parentDir, err)
		}
	}
	return nil
}

func sqliteParentDir(dsn string) (string, bool) {
	path, ok := sqliteLocalPath(dsn)
	if !ok {
		return "", false
	}
	return filepath.Dir(path), true
}

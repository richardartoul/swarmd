package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/agent"
	cpstore "github.com/richardartoul/swarmd/pkg/server/store"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/memfs"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

type WorkerDriverFactory interface {
	NewWorkerDriver(ctx context.Context, agent cpstore.RunnableAgent) (agent.Driver, error)
}

type RuntimeManager struct {
	Store         *cpstore.Store
	DriverFactory WorkerDriverFactory
	PollInterval  time.Duration
	Stdout        io.Writer
	Stderr        io.Writer
	Logger        *RuntimeLogger
	EnvLookup     func(string) string

	mu      sync.Mutex
	workers map[string]*workerHandle
}

type workerHandle struct {
	key         string
	fingerprint string
	cancel      context.CancelFunc
	done        chan error
}

func (m *RuntimeManager) Run(ctx context.Context) error {
	if m.Store == nil {
		return fmt.Errorf("server runtime manager requires a store")
	}
	if m.DriverFactory == nil {
		return fmt.Errorf("server runtime manager requires a worker driver factory")
	}
	pollInterval := m.PollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	defer m.stopAll()

	if err := m.SyncOnce(ctx); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.SyncOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (m *RuntimeManager) SyncOnce(ctx context.Context) error {
	m.reapWorkers()

	runnableAgents, err := m.Store.ListRunnableAgents(ctx)
	if err != nil {
		return err
	}
	desired := make(map[string]cpstore.RunnableAgent, len(runnableAgents))
	for _, record := range runnableAgents {
		key := workerKey(record.NamespaceID, record.ID)
		desired[key] = record
	}

	m.mu.Lock()
	currentWorkers := make(map[string]*workerHandle, len(m.workers))
	for key, handle := range m.workers {
		currentWorkers[key] = handle
	}
	m.mu.Unlock()

	for key, handle := range currentWorkers {
		record, ok := desired[key]
		if !ok {
			handle.cancel()
			m.mu.Lock()
			delete(m.workers, key)
			m.mu.Unlock()
			continue
		}
		if fingerprint := m.workerFingerprint(record); fingerprint != handle.fingerprint {
			handle.cancel()
			m.mu.Lock()
			delete(m.workers, key)
			m.mu.Unlock()
		}
	}

	for key, record := range desired {
		if m.hasWorker(key, m.workerFingerprint(record)) {
			continue
		}
		if err := m.startWorker(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (m *RuntimeManager) startWorker(ctx context.Context, record cpstore.RunnableAgent) error {
	driver, err := m.DriverFactory.NewWorkerDriver(ctx, record)
	if err != nil {
		return fmt.Errorf("create driver for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	runtimeConfig, err := loadManagedAgentRuntimeConfig(record.ConfigJSON)
	if err != nil {
		return fmt.Errorf("decode managed config for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	filesystem := runtimeConfig.filesystemSettings()
	memory := runtimeConfig.memorySettings()
	mounts := runtimeConfig.mountSettings()
	network := runtimeConfig.networkSettings()
	capabilities := runtimeConfig.capabilities()
	tools := runtimeConfig.toolSettings()
	httpHeaders := runtimeConfig.httpHeaderSettings()
	resolvedHTTPHeaders, err := resolveManagedHTTPHeaderRules(httpHeaders, m.envLookup())
	if err != nil {
		return fmt.Errorf("resolve HTTP headers for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	systemPrompt := composeManagedSystemPrompt(record, capabilities, memory, mounts, network, httpHeaders)
	queue := &MessageQueue{
		Store:         m.Store,
		NamespaceID:   record.NamespaceID,
		AgentID:       record.ID,
		PollInterval:  250 * time.Millisecond,
		LeaseOwner:    m.Store.LeaseOwner(),
		LeaseDuration: record.LeaseDuration,
		SystemPrompt:  systemPrompt,
		Logger:        m.Logger,
	}
	globalReachableHosts := resolveManagedNetworkHostMatchers(network)
	var fsys sandbox.FileSystem
	switch filesystem.kind() {
	case managedAgentFilesystemKindDisk:
		fsys, err = sandbox.NewFS(record.RootPath)
	case managedAgentFilesystemKindMemory:
		fsys, err = memfs.New(record.RootPath)
	default:
		err = fmt.Errorf("unsupported filesystem kind %q", filesystem.kind())
	}
	if err != nil {
		return fmt.Errorf(
			"construct %s filesystem for worker %q/%q: %w",
			filesystem.kind(),
			record.NamespaceID,
			record.ID,
			err,
		)
	}
	if err := agent.SweepStaleOutputSpillDirs(fsys); err != nil {
		return fmt.Errorf("sweep stale spill directories for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	if err := materializeAgentMounts(fsys, mounts, m.envLookup()); err != nil {
		return fmt.Errorf("apply mounts for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}
	runtime, err := agent.New(agent.Config{
		FileSystem:           fsys,
		NetworkDialer:        interp.OSNetworkDialer{},
		GlobalReachableHosts: globalReachableHosts,
		HTTPHeaders:          resolvedHTTPHeaders,
		ConfiguredTools:      tools,
		ToolRuntimeData:      newServerToolRuntime(record.NamespaceID, record.ID, m.envLookup(), m.Logger),
		Queue:                queue,
		Driver:               driver,
		SystemPrompt:         systemPrompt,
		OnStep:               StepPersister{Store: m.Store, Logger: m.Logger},
		OnResult: ResultPersister{
			Store:            m.Store,
			RetryDelay:       record.RetryDelay,
			Logger:           m.Logger,
			AllowMessageSend: capabilityBool(capabilities, capabilityAllowMessageSend),
		},
		MaxSteps:                     record.MaxSteps,
		StepTimeout:                  record.StepTimeout,
		MaxOutputBytes:               record.MaxOutputBytes,
		OutputFileThresholdBytes:     runtimeConfig.outputFileThresholdBytes(),
		PreserveStateBetweenTriggers: record.PreserveState,
		Stdout:                       m.Stdout,
		Stderr:                       m.Stderr,
	})
	if err != nil {
		return fmt.Errorf("construct runtime for worker %q/%q: %w", record.NamespaceID, record.ID, err)
	}

	key := workerKey(record.NamespaceID, record.ID)
	workerCtx, cancel := context.WithCancel(context.Background())
	handle := &workerHandle{
		key:         key,
		fingerprint: m.workerFingerprint(record),
		cancel:      cancel,
		done:        make(chan error, 1),
	}

	m.mu.Lock()
	if m.workers == nil {
		m.workers = make(map[string]*workerHandle)
	}
	m.workers[key] = handle
	m.mu.Unlock()

	go func() {
		err := runtime.Serve(workerCtx)
		if err != nil && errors.Is(err, context.Canceled) {
			err = nil
		}
		if closeErr := runtime.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		handle.done <- err
		close(handle.done)
	}()
	return nil
}

func (m *RuntimeManager) reapWorkers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, handle := range m.workers {
		select {
		case err, ok := <-handle.done:
			if ok && err != nil {
				// Leave the worker absent from the active set so the next sync restarts it.
			}
			delete(m.workers, key)
		default:
		}
	}
}

func (m *RuntimeManager) stopAll() {
	m.mu.Lock()
	workers := make([]*workerHandle, 0, len(m.workers))
	for _, handle := range m.workers {
		workers = append(workers, handle)
	}
	m.workers = make(map[string]*workerHandle)
	m.mu.Unlock()

	for _, handle := range workers {
		handle.cancel()
	}
	for _, handle := range workers {
		<-handle.done
	}
}

func (m *RuntimeManager) hasWorker(key, fingerprint string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	handle, ok := m.workers[key]
	return ok && handle.fingerprint == fingerprint
}

func (m *RuntimeManager) workerFingerprint(record cpstore.RunnableAgent) string {
	return workerFingerprintWithEnv(record, m.envLookup())
}

func (m *RuntimeManager) envLookup() func(string) string {
	if m.EnvLookup != nil {
		return m.EnvLookup
	}
	return os.Getenv
}

func workerKey(namespaceID, agentID string) string {
	return namespaceID + ":" + agentID
}

func workerFingerprint(record cpstore.RunnableAgent) string {
	return workerFingerprintWithEnv(record, os.Getenv)
}

func workerFingerprintWithEnv(record cpstore.RunnableAgent, lookupEnv func(string) string) string {
	return fmt.Sprintf(
		"%s|%s|%s|%s|%d|%d|%d|%d|%d|%s|%s|%s|%s",
		record.CurrentPromptVersionID,
		record.RootPath,
		record.ModelProvider,
		record.ModelName,
		record.UpdatedAt.UnixMilli(),
		record.MaxSteps,
		record.StepTimeout.Milliseconds(),
		record.MaxOutputBytes,
		record.LeaseDuration.Milliseconds(),
		record.ModelBaseURL,
		record.SystemPrompt,
		record.ConfigJSON,
		managedAgentRuntimeEnvHash(record.ConfigJSON, lookupEnv),
	)
}

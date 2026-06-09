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

// WorkerDriverFactory constructs the model driver for one worker agent.
type WorkerDriverFactory interface {
	NewWorkerDriver(ctx context.Context, agent cpstore.RunnableAgent) (agent.Driver, error)
}

// RuntimeManager supervises one worker goroutine per runnable agent,
// reconciling the running set against the store on every poll interval.
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
	fingerprint string
	cancel      context.CancelFunc
	done        chan error
}

// Run reconciles workers against the store until ctx is canceled.
//
// Reconciliation errors are logged and retried on the next tick rather than
// returned: a transient store failure or one misconfigured agent must not
// take down every other worker in the server.
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

	for {
		if err := m.SyncOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.Logger.LogComponentError("manager", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// SyncOnce reconciles running workers against the desired set once.
//
// Per-agent start failures are isolated: they are logged and retried on the
// next sync instead of failing the reconciliation, so one bad agent spec or
// missing credential cannot block every other agent.
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

	for key, handle := range m.snapshotWorkers() {
		record, ok := desired[key]
		if ok && m.workerFingerprint(record) == handle.fingerprint {
			continue
		}
		m.stopWorker(key, handle)
	}

	for key, record := range desired {
		if m.hasWorker(key, m.workerFingerprint(record)) {
			continue
		}
		if err := m.startWorker(ctx, record); err != nil {
			m.Logger.LogWorkerStartError(record.NamespaceID, record.ID, err)
		}
	}
	return nil
}

func (m *RuntimeManager) snapshotWorkers() map[string]*workerHandle {
	m.mu.Lock()
	defer m.mu.Unlock()
	workers := make(map[string]*workerHandle, len(m.workers))
	for key, handle := range m.workers {
		workers[key] = handle
	}
	return workers
}

// stopWorker cancels a worker and waits for it to exit before removing it
// from the active set. Waiting matters: the replacement worker shares the
// old one's sandbox root and lease owner, so the two must never overlap.
func (m *RuntimeManager) stopWorker(key string, handle *workerHandle) {
	handle.cancel()
	<-handle.done
	m.mu.Lock()
	delete(m.workers, key)
	m.mu.Unlock()
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

// reapWorkers removes exited workers from the active set so the next sync
// restarts them if they are still desired.
func (m *RuntimeManager) reapWorkers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, handle := range m.workers {
		select {
		case <-handle.done:
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

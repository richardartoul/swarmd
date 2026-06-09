// Package server hosts YAML-defined agents as supervised workers backed by a
// SQLite control plane.
//
// The moving parts:
//
//   - Agent specs are loaded from a config root (namespaces/<ns>/agents/*.yaml)
//     and reconciled into the store by [SyncSpecsFromConfigRoot]; [PlanSyncFromConfigRoot]
//     renders the same reconciliation as a dry-run plan.
//   - [RuntimeManager] level-triggers the desired worker set against the store:
//     one goroutine per runnable agent, each wrapping a sandboxed [agent.Agent]
//     whose triggers come from a [MessageQueue] lease on the mailbox.
//   - [Scheduler] fires cron schedules by enqueueing mailbox messages.
//   - [StepPersister] and [ResultPersister] record per-step and per-run state;
//     the persister also enforces outbox policy (capabilities, recipient
//     existence) and converts violations into dead-lettered messages rather
//     than worker failures.
//
// Worker lifecycles are isolated: a single agent that cannot start, or a
// transient store error, never takes down the manager or its siblings.
package server

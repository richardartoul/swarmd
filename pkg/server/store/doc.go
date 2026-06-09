// Package store is the SQLite-backed control plane for the swarmd server:
// namespaces, agents, prompt versions, cron schedules, mailbox messages,
// runs, and per-run steps.
//
// Message delivery uses lease-based claiming. A claim transaction moves a
// message to the leased state, increments its attempt count, and creates the
// run record; completion either completes the message, requeues it with a
// retry delay, or dead-letters it. Messages whose lease expired without a
// recorded result are reclaimable, and the claim path retires messages whose
// attempts are exhausted so an unpersistable message can never re-run its
// agent forever.
//
// Schema migrations run on a dedicated connection because the foreign_keys
// pragma is connection-scoped; see [Store.Migrate].
package store

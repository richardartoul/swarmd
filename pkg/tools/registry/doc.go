// Package registry is the process-wide catalog of structured tools.
//
// Built-in tools register themselves at init time and are exposed to every
// agent, gated only by capability requirements such as network access.
// Custom tools also register at init time but reach an agent only when its
// spec lists them explicitly.
//
// Each registration declares a network scope: "none" (no outbound access),
// "global" (the agent's reachable-hosts policy), or "scoped" (the union of
// the global policy and the tool's declared required hosts). The registry
// validates scopes at registration time and resolves per-agent bindings at
// construction time.
package registry

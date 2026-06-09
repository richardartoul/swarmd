// Package core defines the contracts shared by the agent runtime and every
// structured tool: tool definitions and wire shapes, step records, the
// sandbox-safe [ToolContext] surface handed to handlers, and the
// [ToolPlugin] registration interface.
//
// It intentionally has no dependencies on the runtime or on individual tool
// implementations so that both sides can depend on it without cycles.
package core

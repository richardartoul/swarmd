// Package common provides the shared building blocks for structured tool
// implementations:
//
//   - schema.go: JSON-schema constructors for tool parameter definitions
//   - toolconfig.go: tool configuration decoding and output marshaling
//   - text.go: bounded text, line, and duration helpers
//   - http.go: tool HTTP plumbing with an enforced response-body lifecycle
//   - html.go: HTML text extraction helpers
//   - pagination.go: stable windowing over result lists
//   - output_compaction.go: structured compaction of oversized JSON outputs
//   - identifiers.go: identifier validation shared with the registry
//
// Keep this package scoped to helpers used by two or more tools; anything
// tool-specific belongs in that tool's own package.
package common

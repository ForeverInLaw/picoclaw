// Package facts provides mem0-style atomic-fact memory for PicoClaw agents.
//
// The package is independent of the conversation history store in
// pkg/memory. Facts are atomic (entity, attribute, value) tuples scoped
// by namespace, with embedding-backed semantic recall and async LLM
// extraction.
//
// See docs/design/2026-05-20-telegram-memory-design.md for the design spec.
package facts

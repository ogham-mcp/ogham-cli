package outbox

import "time"

// Record is the serialized payload of one buffered hook event.
// Written as one JSON line per file in the outbox directory. The
// downstream drainer hands a *Record to a handler func that posts it
// to the memory store; on handler success the file is deleted.
//
// Schema version is included so a future incompatible change can be
// detected by an older drainer and quarantined rather than
// mis-parsed.
type Record struct {
	SchemaVersion int       `json:"schema_version"`
	EnqueuedAt    time.Time `json:"enqueued_at"`

	// Event payload mirrors what hooks.py post_tool eventually
	// passes to store_memory_enriched.
	Content string   `json:"content"`
	Profile string   `json:"profile"`
	Source  string   `json:"source"`
	Tags    []string `json:"tags"`

	// Provenance for debugging + audit. The drainer doesn't act on
	// these but they show up if the file is inspected by hand.
	SessionID string `json:"session_id,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}

// CurrentSchemaVersion is the schema version we serialize today.
// Bump if Record changes incompatibly; the drainer logs and skips
// records with a higher version so a partially-rolled fleet
// gracefully degrades.
const CurrentSchemaVersion = 1

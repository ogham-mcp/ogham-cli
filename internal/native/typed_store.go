package native

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Typed-store wrappers mirror the Python ogham-mcp tools
// (store_decision, store_fact, store_event, store_preference). Each is
// a thin shell over Store() that:
//
//  1. Formats the structured inputs into multi-line prose content
//     (matches Python exactly -- search queries trained on the old
//     shape still return the same results).
//  2. Injects a `type:<kind>` tag for tag-filtered retrieval, while
//     preserving any extra tags the caller supplied.
//  3. Sets the structured `metadata` jsonb payload so later queries
//     can filter by `when`, `subject`, `confidence` etc. without
//     re-parsing the prose body.
//
// IMPORTANT: tags (text[]) and metadata (jsonb) are separate columns
// in the memories table. The hybrid search path filters on tags;
// metadata is rich context accessed at result-display time. Keep the
// two distinct.

// storeDecisionBatchRelated flips to true once CreateRelationship is
// absorbed natively. With Batch E landed, store_decision creates
// supports-type edges to each related_memories UUID after the INSERT.
const storeDecisionBatchRelated = true

// -----------------------------------------------------------------------
// store_decision

type StoreDecisionOptions struct {
	Decision        string
	Rationale       string
	Alternatives    []string
	ReasoningTrace  string
	Tags            []string
	RelatedMemories []string // rejected until Batch E lands
	Source          string
	Profile         string
}

func StoreDecision(ctx context.Context, cfg *Config, opts StoreDecisionOptions) (*StoreResult, error) {
	if strings.TrimSpace(opts.Decision) == "" {
		return nil, fmt.Errorf("store_decision: decision is required")
	}
	if strings.TrimSpace(opts.Rationale) == "" {
		return nil, fmt.Errorf("store_decision: rationale is required")
	}
	if len(opts.RelatedMemories) > 0 && !storeDecisionBatchRelated {
		return nil, fmt.Errorf(
			"store_decision: related_memories linking is not yet absorbed natively (pending Batch E graph walk) -- " +
				"route this call through the Python sidecar or store the decision first and link manually")
	}

	parts := []string{
		"Decision: " + opts.Decision,
		"Rationale: " + opts.Rationale,
	}
	if len(opts.Alternatives) > 0 {
		parts = append(parts, "Alternatives considered: "+strings.Join(opts.Alternatives, ", "))
	}
	if opts.ReasoningTrace != "" {
		parts = append(parts, "Reasoning: "+opts.ReasoningTrace)
	}
	content := strings.Join(parts, "\n")

	metadata := map[string]any{
		"type":         "decision",
		"alternatives": nonNilSlice(opts.Alternatives),
		"decided_at":   time.Now().UTC().Format(time.RFC3339),
	}
	if opts.ReasoningTrace != "" {
		metadata["reasoning_trace"] = opts.ReasoningTrace
	}

	result, err := Store(ctx, cfg, content, StoreOptions{
		Tags:     appendTypeTag(opts.Tags, "type:decision"),
		Source:   opts.Source,
		Profile:  opts.Profile,
		Metadata: metadata,
	})
	if err != nil {
		return nil, err
	}

	// Post-store edge creation. Each related_memories UUID becomes a
	// 'supports' edge from the new decision to the referenced memory.
	// Best-effort: a single edge failure doesn't roll back the store,
	// but we do surface the error to the caller (parity with Python
	// where a raising db_create_relationship propagates up).
	for _, relID := range opts.RelatedMemories {
		if relID == "" {
			continue
		}
		if err := CreateRelationship(ctx, cfg, CreateRelationshipOptions{
			SourceID:     result.ID,
			TargetID:     relID,
			Relationship: "supports",
			Strength:     1.0,
			CreatedBy:    "user",
		}); err != nil {
			return result, fmt.Errorf("store_decision: link %s: %w", relID, err)
		}
	}
	return result, nil
}

// -----------------------------------------------------------------------
// store_fact

type StoreFactOptions struct {
	Fact           string
	Subject        string
	Confidence     float64 // default 1.0; validated [0.0, 1.0]
	SourceCitation string
	Tags           []string
	Source         string
	Profile        string
}

func StoreFact(ctx context.Context, cfg *Config, opts StoreFactOptions) (*StoreResult, error) {
	if strings.TrimSpace(opts.Fact) == "" {
		return nil, fmt.Errorf("store_fact: fact is required")
	}
	conf := opts.Confidence
	if conf == 0 {
		conf = 1.0 // JSON zero-value default. Parity with Python default.
	}
	if conf < 0 || conf > 1 {
		return nil, fmt.Errorf("store_fact: confidence must be in [0.0, 1.0]; got %v", conf)
	}

	parts := []string{"Fact: " + opts.Fact}
	if opts.Subject != "" {
		parts = append(parts, "Subject: "+opts.Subject)
	}
	if opts.SourceCitation != "" {
		parts = append(parts, "Source: "+opts.SourceCitation)
	}
	content := strings.Join(parts, "\n")

	metadata := map[string]any{
		"type":            "fact",
		"subject":         opts.Subject,
		"confidence":      conf,
		"source_citation": opts.SourceCitation,
		"recorded_at":     time.Now().UTC().Format(time.RFC3339),
	}

	return Store(ctx, cfg, content, StoreOptions{
		Tags:     appendTypeTag(opts.Tags, "type:fact"),
		Source:   opts.Source,
		Profile:  opts.Profile,
		Metadata: metadata,
	})
}

// -----------------------------------------------------------------------
// store_event

type StoreEventOptions struct {
	Event        string
	When         string
	Participants []string
	Location     string
	Tags         []string
	Source       string
	Profile      string
}

func StoreEvent(ctx context.Context, cfg *Config, opts StoreEventOptions) (*StoreResult, error) {
	if strings.TrimSpace(opts.Event) == "" {
		return nil, fmt.Errorf("store_event: event is required")
	}

	parts := []string{"Event: " + opts.Event}
	if opts.When != "" {
		parts = append(parts, "When: "+opts.When)
	}
	if len(opts.Participants) > 0 {
		parts = append(parts, "Participants: "+strings.Join(opts.Participants, ", "))
	}
	if opts.Location != "" {
		parts = append(parts, "Location: "+opts.Location)
	}
	content := strings.Join(parts, "\n")

	metadata := map[string]any{
		"type":         "event",
		"when":         opts.When,
		"participants": nonNilSlice(opts.Participants),
		"location":     opts.Location,
		"recorded_at":  time.Now().UTC().Format(time.RFC3339),
	}

	return Store(ctx, cfg, content, StoreOptions{
		Tags:     appendTypeTag(opts.Tags, "type:event"),
		Source:   opts.Source,
		Profile:  opts.Profile,
		Metadata: metadata,
	})
}

// -----------------------------------------------------------------------
// store_preference

// validPreferenceStrengths gates the strength arg. Python raises on
// anything outside this set; Go mirrors that with an error return.
var validPreferenceStrengths = map[string]bool{
	"strong": true,
	"normal": true,
	"weak":   true,
}

type StorePreferenceOptions struct {
	Preference   string
	Subject      string
	Alternatives []string
	Strength     string // "strong" | "normal" | "weak"; default "normal"
	Tags         []string
	Source       string
	Profile      string
}

func StorePreference(ctx context.Context, cfg *Config, opts StorePreferenceOptions) (*StoreResult, error) {
	if strings.TrimSpace(opts.Preference) == "" {
		return nil, fmt.Errorf("store_preference: preference is required")
	}
	strength := opts.Strength
	if strength == "" {
		strength = "normal"
	}
	if !validPreferenceStrengths[strength] {
		return nil, fmt.Errorf("store_preference: strength must be one of: strong, normal, weak; got %q", strength)
	}

	parts := []string{"Preference: " + opts.Preference}
	if opts.Subject != "" {
		parts = append(parts, "Subject: "+opts.Subject)
	}
	if len(opts.Alternatives) > 0 {
		parts = append(parts, "Rejected alternatives: "+strings.Join(opts.Alternatives, ", "))
	}
	parts = append(parts, "Strength: "+strength)
	content := strings.Join(parts, "\n")

	metadata := map[string]any{
		"type":         "preference",
		"subject":      opts.Subject,
		"alternatives": nonNilSlice(opts.Alternatives),
		"strength":     strength,
		"recorded_at":  time.Now().UTC().Format(time.RFC3339),
	}

	return Store(ctx, cfg, content, StoreOptions{
		Tags:     appendTypeTag(opts.Tags, "type:preference"),
		Source:   opts.Source,
		Profile:  opts.Profile,
		Metadata: metadata,
	})
}

// -----------------------------------------------------------------------
// helpers

// appendTypeTag returns a copy of user with typeTag appended unless it's
// already present. Never mutates user -- the caller may reuse their
// slice. Idempotent for repeated calls with the same typeTag.
func appendTypeTag(user []string, typeTag string) []string {
	for _, t := range user {
		if t == typeTag {
			// Copy to avoid aliasing; downstream Store mutates.
			out := make([]string, len(user))
			copy(out, user)
			return out
		}
	}
	out := make([]string, 0, len(user)+1)
	out = append(out, user...)
	out = append(out, typeTag)
	return out
}

// nonNilSlice returns an empty []string when the input is nil. The
// typed-store metadata payloads need "alternatives" / "participants" to
// round-trip as JSON arrays, not nulls, so dashboards and LLM consumers
// see `"alternatives": []` on decisions that had no rejected options.
func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

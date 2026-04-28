package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWalkKnowledge_NilCfg(t *testing.T) {
	if _, err := WalkKnowledge(context.Background(), nil, "id", WalkKnowledgeOptions{}); err == nil {
		t.Error("expected error on nil config")
	}
}

func TestWalkKnowledge_EmptyStartID(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: "postgres://x"}}
	if _, err := WalkKnowledge(context.Background(), cfg, "", WalkKnowledgeOptions{}); err == nil {
		t.Error("expected error on empty start_id")
	}
}

func TestWalkKnowledge_DepthClamp(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: "postgres://x"}}
	if _, err := WalkKnowledge(context.Background(), cfg, "id", WalkKnowledgeOptions{Depth: 6}); err == nil {
		t.Error("expected error when depth > 5")
	}
}

func TestWalkKnowledge_BadDirection(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "postgres", URL: "postgres://x"}}
	if _, err := WalkKnowledge(context.Background(), cfg, "id", WalkKnowledgeOptions{Direction: "sideways"}); err == nil {
		t.Error("expected error on invalid direction")
	}
}

func TestWalkKnowledge_UnknownBackend(t *testing.T) {
	cfg := &Config{Database: Database{Backend: "rocks"}}
	if _, err := WalkKnowledge(context.Background(), cfg, "id", WalkKnowledgeOptions{}); err == nil {
		t.Error("expected error on unknown backend")
	}
}

// PICT-style: every (direction, depth, types-empty/nonempty) combination
// gets through the param building. The Supabase happy path exercises
// each enum value to ensure the RPC arg shape is right.
func TestWalkKnowledge_SupabaseHappyPath(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		depth    int
		types    []string
		expectIn map[string]any
	}{
		{
			name:  "outgoing depth 2 with types",
			dir:   "outgoing",
			depth: 2,
			types: []string{"depends_on", "cites"},
			expectIn: map[string]any{
				"p_start_id":           "11111111-1111-1111-1111-111111111111",
				"p_max_depth":          float64(2),
				"p_direction":          "outgoing",
				"p_min_strength":       float64(0),
				"p_result_limit":       float64(50),
				"p_relationship_types": []any{"depends_on", "cites"},
			},
		},
		{
			name:  "incoming depth 1 no types",
			dir:   "incoming",
			depth: 1,
			expectIn: map[string]any{
				"p_start_id":     "11111111-1111-1111-1111-111111111111",
				"p_max_depth":    float64(1),
				"p_direction":    "incoming",
				"p_min_strength": float64(0),
				"p_result_limit": float64(50),
			},
		},
		{
			name: "default direction both, default depth 1",
			expectIn: map[string]any{
				"p_start_id":     "11111111-1111-1111-1111-111111111111",
				"p_max_depth":    float64(1),
				"p_direction":    "both",
				"p_min_strength": float64(0),
				"p_result_limit": float64(50),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/rpc/wiki_walk_graph") {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				var args map[string]any
				if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
					t.Fatalf("decode args: %v", err)
				}
				for k, v := range tc.expectIn {
					if got, ok := args[k]; !ok {
						t.Errorf("missing arg %q", k)
					} else if !equalJSONish(got, v) {
						t.Errorf("arg %q = %#v, want %#v", k, got, v)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[
					{
						"id": "22222222-2222-2222-2222-222222222222",
						"content": "neighbour",
						"metadata": {"k":"v"},
						"source": "claude",
						"tags": ["a","b"],
						"confidence": 0.9,
						"depth": 1,
						"relationship": "depends_on",
						"edge_strength": 0.7,
						"connected_from": "11111111-1111-1111-1111-111111111111",
						"direction_used": "outgoing"
					}
				]`))
			}))
			defer server.Close()

			cfg := &Config{
				Database:  Database{Backend: "supabase", SupabaseURL: server.URL, SupabaseKey: "test"},
				Embedding: Embedding{Provider: "gemini", APIKey: "k"},
				Profile:   "default",
			}
			res, err := WalkKnowledge(context.Background(), cfg,
				"11111111-1111-1111-1111-111111111111",
				WalkKnowledgeOptions{
					Depth:             tc.depth,
					Direction:         tc.dir,
					RelationshipTypes: tc.types,
				})
			if err != nil {
				t.Fatalf("WalkKnowledge: %v", err)
			}
			if res.NodeCount != 1 || len(res.Nodes) != 1 {
				t.Fatalf("expected 1 node, got %d", res.NodeCount)
			}
			n := res.Nodes[0]
			if n.Relationship != "depends_on" {
				t.Errorf("relationship = %q", n.Relationship)
			}
			if n.EdgeStrength != 0.7 {
				t.Errorf("edge_strength = %f", n.EdgeStrength)
			}
			if n.DirectionUsed != "outgoing" {
				t.Errorf("direction_used = %q", n.DirectionUsed)
			}
			if n.Metadata["k"] != "v" {
				t.Errorf("metadata roundtrip lost: %#v", n.Metadata)
			}
		})
	}
}

// equalJSONish compares two values produced by json.Unmarshal; lists
// are compared element-wise so the slice-vs-array mismatch in
// reflect.DeepEqual on heterogeneous types doesn't trip us up.
func equalJSONish(a, b any) bool {
	switch av := a.(type) {
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return false
		}
		if len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !equalJSONish(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

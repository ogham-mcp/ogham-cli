package extraction

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestEntities_EachType verifies that every pattern category produces the
// expected prefix and value for a canonical input.
func TestEntities_EachType(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string // subset that MUST be present (sorted checked separately)
	}{
		{
			name:    "CamelCase identifier",
			content: "Deploying the PaymentGateway service tomorrow.",
			want:    []string{"entity:PaymentGateway"},
		},
		{
			name:    "Relative file path",
			content: "Edit ./cmd/root.go to add the flag.",
			want:    []string{"file:./cmd/root.go"},
		},
		{
			name:    "Error type suffix",
			content: "ConnectionRefusedError bubbled up from the driver.",
			want:    []string{"error:ConnectionRefusedError"},
		},
		{
			name:    "Exception suffix",
			content: "RuntimeException thrown during pool warmup.",
			want:    []string{"error:RuntimeException"},
		},
		{
			name:    "Person name",
			content: "Spoke with Kevin Burns about the release.",
			want:    []string{"person:Kevin Burns"},
		},
		{
			name:    "Person name with trailing punctuation",
			content: "Kevin Burns, Iain McLeod and Andres Garcia agreed.",
			want: []string{
				"person:Kevin Burns",
				"person:Iain McLeod",
				"person:Andres Garcia",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Entities(tc.content)
			gotSet := map[string]struct{}{}
			for _, e := range got {
				gotSet[e] = struct{}{}
			}
			for _, w := range tc.want {
				if _, ok := gotSet[w]; !ok {
					t.Errorf("missing %q in %v", w, got)
				}
			}
		})
	}
}

// TestEntities_StopwordFilter verifies that "The Company" and "Of Course"
// do NOT emit person: tags -- parity with Python's stopword check.
func TestEntities_StopwordFilter(t *testing.T) {
	content := "The Company is Of Course fine. But She Said something."
	got := Entities(content)
	for _, e := range got {
		if strings.HasPrefix(e, "person:") {
			t.Errorf("unexpected person: tag from stopword pair: %q", e)
		}
	}
}

// TestEntities_Deduplicated verifies that repeated entities collapse.
func TestEntities_Deduplicated(t *testing.T) {
	content := "PaymentGateway and PaymentGateway again, then PaymentGateway."
	got := Entities(content)
	count := 0
	for _, e := range got {
		if e == "entity:PaymentGateway" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("entity:PaymentGateway appeared %d times, want 1", count)
	}
}

// TestEntities_FilePathCap verifies the per-category file path cap (5) --
// mirrors Python's `if i >= 5: break` in _FILE_PATH.finditer.
func TestEntities_FilePathCap(t *testing.T) {
	content := `
./a/one.go ./b/two.go ./c/three.go ./d/four.go
./e/five.go ./f/six.go ./g/seven.go
`
	got := Entities(content)
	fileCount := 0
	for _, e := range got {
		if strings.HasPrefix(e, "file:") {
			fileCount++
		}
	}
	if fileCount > filePathCap {
		t.Errorf("file: tags = %d, want <= %d (cap)", fileCount, filePathCap)
	}
}

// TestEntities_SortedOutput verifies the result is sorted ascending --
// Python does sorted(entities) on the final set.
func TestEntities_SortedOutput(t *testing.T) {
	content := "Kevin Burns uses PostgreSQL at ./cmd/root.go"
	got := Entities(content)
	sortedCopy := append([]string(nil), got...)
	sort.Strings(sortedCopy)
	if !reflect.DeepEqual(got, sortedCopy) {
		t.Errorf("output not sorted: %v", got)
	}
}

// TestEntities_MaxCap verifies the 20-entity final cap.
func TestEntities_MaxCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("PaymentGateway")
		// Slight variations to force 40 distinct entity: tags.
		b.WriteByte('A' + byte(i%26))
		b.WriteByte(' ')
	}
	got := Entities(b.String())
	if len(got) > MaxEntities {
		t.Errorf("returned %d entities, want <= %d", len(got), MaxEntities)
	}
}

// TestEntities_EmptyContent returns an empty slice for empty input.
func TestEntities_EmptyContent(t *testing.T) {
	if got := Entities(""); len(got) != 0 {
		t.Errorf("empty content should yield no entities, got %v", got)
	}
}

// TestEntities_NoFalsePositives_CamelCase verifies that plain words and
// ALL-CAPS acronyms do NOT match the CamelCase pattern (mirrors Python
// \b[A-Z][a-z]+(?:[A-Z][a-zA-Z]*)+\b -- needs lowercase-then-uppercase).
func TestEntities_NoFalsePositives_CamelCase(t *testing.T) {
	content := "HTTP and API are acronyms. Hello world. Just one Capital."
	got := Entities(content)
	for _, e := range got {
		if strings.HasPrefix(e, "entity:") {
			t.Errorf("unexpected entity: tag from non-camel input: %q", e)
		}
	}
}

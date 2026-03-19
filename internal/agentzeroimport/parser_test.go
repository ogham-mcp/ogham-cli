package agentzeroimport

import (
	"os"
	"testing"
)

const testPicklePath = "/tmp/agent-zero-memory/default/index.pkl"

func pickleAvailable(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(testPicklePath); os.IsNotExist(err) {
		t.Skipf("pickle not found at %s, skipping", testPicklePath)
	}
}

func TestParsePickle(t *testing.T) {
	pickleAvailable(t)

	memories, err := ParsePickle(testPicklePath, nil, false)
	if err != nil {
		t.Fatalf("ParsePickle failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("expected at least one memory")
	}

	for i, m := range memories {
		if m.Source != "agent-zero" {
			t.Errorf("memory[%d]: source = %q, want %q", i, m.Source, "agent-zero")
		}
		if len(m.Tags) == 0 {
			t.Errorf("memory[%d]: expected at least one tag", i)
		}
		if m.Content == "" {
			t.Errorf("memory[%d]: empty content", i)
		}
	}
}

func TestParsePickleSkipsKnowledge(t *testing.T) {
	pickleAvailable(t)

	withoutKnowledge, err := ParsePickle(testPicklePath, nil, false)
	if err != nil {
		t.Fatalf("ParsePickle (no knowledge) failed: %v", err)
	}

	withKnowledge, err := ParsePickle(testPicklePath, nil, true)
	if err != nil {
		t.Fatalf("ParsePickle (with knowledge) failed: %v", err)
	}

	if len(withKnowledge) < len(withoutKnowledge) {
		t.Errorf("with knowledge (%d) should be >= without knowledge (%d)",
			len(withKnowledge), len(withoutKnowledge))
	}
}

func TestParsePickleAreaFilter(t *testing.T) {
	pickleAvailable(t)

	memories, err := ParsePickle(testPicklePath, []string{"fragments"}, false)
	if err != nil {
		t.Fatalf("ParsePickle (fragments) failed: %v", err)
	}

	for i, m := range memories {
		found := false
		for _, tag := range m.Tags {
			if tag == "agent-zero-fragments" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("memory[%d]: expected tag 'agent-zero-fragments', got %v", i, m.Tags)
		}
	}
}

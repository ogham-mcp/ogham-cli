package cmd

import (
	"reflect"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{",,,", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,c", []string{"a", "b", "c"}},
		{"type:decision,project:ogham", []string{"type:decision", "project:ogham"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
		{"abcdefghij", 10, "abcdefghij"},
		{"abcdefghijk", 10, "abcdefghij..."},
		{"日本語テスト", 3, "日本語..."},
	}
	for _, tc := range cases {
		got := truncate(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestExtractMemories(t *testing.T) {
	t.Run("raw array", func(t *testing.T) {
		got := extractMemories([]any{
			map[string]any{"id": "1"},
			map[string]any{"id": "2"},
		})
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
	})
	t.Run("results key", func(t *testing.T) {
		got := extractMemories(map[string]any{
			"results": []any{map[string]any{"id": "x"}},
		})
		if len(got) != 1 || got[0]["id"] != "x" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("result singular key (Python MCP wrapper)", func(t *testing.T) {
		got := extractMemories(map[string]any{
			"result": []any{map[string]any{"id": "y"}, map[string]any{"id": "z"}},
		})
		if len(got) != 2 {
			t.Errorf("got %+v, want 2 memories", got)
		}
	})
	t.Run("unknown shape", func(t *testing.T) {
		if got := extractMemories("stringy"); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})
	t.Run("non-map entries dropped", func(t *testing.T) {
		got := extractMemories([]any{
			map[string]any{"id": "1"},
			"stray",
			map[string]any{"id": "2"},
		})
		if len(got) != 2 {
			t.Errorf("len = %d, want 2 (non-map dropped): %+v", len(got), got)
		}
	})
}

func TestNotImplemented(t *testing.T) {
	err := notImplemented("search")
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() == "" {
		t.Error("error should have a message")
	}
}

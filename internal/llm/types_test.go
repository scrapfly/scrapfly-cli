package llm

import (
	"encoding/json"
	"testing"
)

func TestUsage_JSONTags(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 0}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	// Should be snake_case; cache_write_tokens omitted when 0.
	got := string(b)
	want := `{"input_tokens":10,"output_tokens":5,"cache_read_tokens":3}`
	if got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestRegistry_NewUnknown(t *testing.T) {
	if _, err := New("does-not-exist", Options{}); err == nil {
		t.Error("expected error for unknown provider")
	}
}

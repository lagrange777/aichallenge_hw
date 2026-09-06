package models

import "testing"

func TestAvailableKeepsPowerOrderRegardlessOfDefault(t *testing.T) {
	available := Available("gpt-5.4-mini")
	want := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.3-codex", "gpt-5.4-mini", "gpt-5.6-luna"}
	if len(available) != len(want) {
		t.Fatalf("available models = %#v", available)
	}
	for index, definition := range available {
		if definition.ID != want[index] {
			t.Fatalf("available[%d] = %q, want %q", index, definition.ID, want[index])
		}
	}
}

func TestAvailableKeepsCustomDefault(t *testing.T) {
	available := Available("custom-model")
	if len(available) != len(catalog)+1 || available[len(available)-1].ID != "custom-model" {
		t.Fatalf("available models = %#v", available)
	}
}

func TestEstimateCostSeparatesCachedAndCacheWriteTokens(t *testing.T) {
	cost, ok := EstimateCost("gpt-5.6-terra", 1000, 200, 100, 300)
	if !ok {
		t.Fatal("EstimateCost() did not find model")
	}
	// 700 regular input × $2/M + 200 cached × $0.20/M +
	// 100 cache-write × $2/M × 1.25 + 300 output × $12/M.
	const want = 0.00529
	if cost != want {
		t.Fatalf("cost = %.8f, want %.8f", cost, want)
	}
}

func TestEstimateCostUnknownModel(t *testing.T) {
	if _, ok := EstimateCost("custom-model", 1, 0, 0, 1); ok {
		t.Fatal("EstimateCost() found an unknown model")
	}
}

package contextmanager

import "testing"

func TestDefaultBudgetFor256KContext(t *testing.T) {
	budget, err := NewBudget(ModelContextSpec{
		ModelName:       "test",
		ContextWindow:   256_000,
		MaxOutputTokens: 8_192,
	}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}

	if budget.HardInputLimit != 209_408 {
		t.Fatalf("hard input limit = %d, want 209408", budget.HardInputLimit)
	}
	if budget.SoftCompactLimit != 188_467 {
		t.Fatalf("soft compact limit = %d, want 188467", budget.SoftCompactLimit)
	}
	if budget.SingleToolResultLimit != 8_000 {
		t.Fatalf("single tool result limit = %d, want 8000", budget.SingleToolResultLimit)
	}
	if budget.ToolBatchLimit != 24_000 {
		t.Fatalf("tool batch limit = %d, want 24000", budget.ToolBatchLimit)
	}
}

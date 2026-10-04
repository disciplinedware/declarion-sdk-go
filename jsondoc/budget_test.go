package jsondoc

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestValidateDecodedBudgetRejectsManySmallNodes(t *testing.T) {
	var builder strings.Builder
	builder.WriteByte('{')
	for i := 0; i < 100; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		fmt.Fprintf(&builder, `"k%d":0`, i)
	}
	builder.WriteByte('}')
	if int64(builder.Len()) >= 1024 {
		t.Fatalf("fixture must fit raw byte cap: %d", builder.Len())
	}
	if err := ValidateDecodedBudget([]byte(builder.String()), 1024); !errors.Is(err, ErrLimit) {
		t.Fatalf("many-node budget error = %v", err)
	}
}

func TestValidateDecodedBudgetRejectsInvalidAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{`{"x":`, `{} {}`, `{"x":]`} {
		if err := ValidateDecodedBudget([]byte(raw), 1024); !errors.Is(err, ErrInvalidJSON) {
			t.Errorf("%q error = %v", raw, err)
		}
	}
}

func TestValidateDecodedBudgetAcceptsWithinStructuralBound(t *testing.T) {
	if err := ValidateDecodedBudget([]byte(`{"a":[1,true,null,"x"]}`), 1024); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

func TestMeasureDecodedMemorySharesValidationAccounting(t *testing.T) {
	raw := []byte(`{"a":[1,true,null,"x"]}`)
	cost, err := MeasureDecodedMemory(raw)
	if err != nil {
		t.Fatalf("measure JSON: %v", err)
	}
	if cost <= int64(len(raw)) {
		t.Fatalf("structural cost %d must include decoded overhead beyond %d raw bytes", cost, len(raw))
	}
	if err := ValidateDecodedBudget(raw, cost); err != nil {
		t.Fatalf("validator rejected measured cost: %v", err)
	}
	if err := ValidateDecodedBudget(raw, cost-1); !errors.Is(err, ErrLimit) {
		t.Fatalf("validator below measured cost = %v, want ErrLimit", err)
	}
}

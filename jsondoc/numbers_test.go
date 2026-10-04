package jsondoc

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestValidateNumbersAcceptsSafeValuesAcrossWidths(t *testing.T) {
	for name, value := range map[string]any{
		"int":     int(42),
		"int8":    int8(-42),
		"int16":   int16(42),
		"int32":   int32(-42),
		"int64":   int64(9007199254740991),
		"uint":    uint(42),
		"uint8":   uint8(42),
		"uint16":  uint16(42),
		"uint32":  uint32(42),
		"uint64":  uint64(9007199254740991),
		"uintptr": uintptr(42),
		"float32": float32(1.25),
		"float64": -1.25,
		"number":  json.Number("9007199254740991.0"),
		"zero":    json.Number("0e1000000000"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNumbers(value); err != nil {
				t.Fatalf("ValidateNumbers(%T(%v)) = %v", value, value, err)
			}
		})
	}
}

func TestValidateNumbersRejectsUnsafeIntegersAcrossWidths(t *testing.T) {
	for name, value := range map[string]any{
		"int64_positive":  int64(9007199254740992),
		"int64_negative":  int64(-9007199254740992),
		"uint64":          uint64(9007199254740992),
		"float32_integer": float32(1 << 53),
		"float64_integer": float64(1 << 53),
		"number_positive": json.Number("9007199254740992"),
		"number_negative": json.Number("-9007199254740992.0"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNumbers(value); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("ValidateNumbers(%T(%v)) = %v, want ErrInvalidJSON", value, value, err)
			}
		})
	}
}

func TestValidateNumbersRejectsInvalidNonfiniteAndUnderflowNumbers(t *testing.T) {
	for name, value := range map[string]any{
		"nan":                 math.NaN(),
		"positive_infinity":   math.Inf(1),
		"negative_infinity":   math.Inf(-1),
		"number_empty":        json.Number(""),
		"number_syntax":       json.Number("1.2.3"),
		"number_leading_zero": json.Number("01"),
		"number_plus_sign":    json.Number("+1"),
		"number_nan":          json.Number("NaN"),
		"number_overflow":     json.Number("1e1000000000"),
		"number_underflow":    json.Number("1e-1000000000"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNumbers(value); !errors.Is(err, ErrInvalidJSON) {
				t.Fatalf("ValidateNumbers(%T(%v)) = %v, want ErrInvalidJSON", value, value, err)
			}
		})
	}
}

func TestValidateNumbersChecksNestedContainersAndCycles(t *testing.T) {
	valid := map[string][]int64{"values": {1, -2, 9007199254740991}}
	if err := ValidateNumbers(valid); err != nil {
		t.Fatalf("safe nested numbers rejected: %v", err)
	}
	invalid := map[string]any{"outer": []any{map[string]uint64{"large": 9007199254740992}}}
	if err := ValidateNumbers(invalid); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("unsafe nested number error = %v, want ErrInvalidJSON", err)
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if err := ValidateNumbers(cycle); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("cyclic value error = %v, want ErrInvalidJSON", err)
	}
	var pointerCycle any
	pointer := &pointerCycle
	pointerCycle = pointer
	if err := ValidateNumbers(pointer); !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("pointer cycle error = %v, want ErrInvalidJSON", err)
	}
}

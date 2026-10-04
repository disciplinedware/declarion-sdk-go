package jsondoc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
)

const maxSafeJSONInteger = int64(1<<53 - 1)

type containerVisit struct {
	typeOf reflect.Type
	ptr    uintptr
}

// ValidateNumbers rejects numeric values that cannot retain the platform's
// safe JSON number identity when represented as binary64.
func ValidateNumbers(value any) error {
	return validateNumbers(reflect.ValueOf(value), make(map[containerVisit]bool))
}

func validateNumbers(value reflect.Value, visiting map[containerVisit]bool) error {
	if !value.IsValid() {
		return nil
	}
	if value.CanInterface() {
		if number, ok := value.Interface().(json.Number); ok {
			return validateJSONNumber(number.String())
		}
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateNumbers(value.Elem(), visiting)
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		visit := containerVisit{typeOf: value.Type(), ptr: uintptr(value.UnsafePointer())}
		if visiting[visit] {
			return fmt.Errorf("cyclic JSON value: %w", ErrInvalidJSON)
		}
		visiting[visit] = true
		defer delete(visiting, visit)
		return validateNumbers(value.Elem(), visiting)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := value.Int()
		if integer < -maxSafeJSONInteger || integer > maxSafeJSONInteger {
			return fmt.Errorf("integer is outside the safe JSON range: %w", ErrInvalidJSON)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if value.Uint() > uint64(maxSafeJSONInteger) {
			return fmt.Errorf("integer is outside the safe JSON range: %w", ErrInvalidJSON)
		}
	case reflect.Float32, reflect.Float64:
		if err := validateFloat(value.Float()); err != nil {
			return err
		}
	case reflect.Complex64, reflect.Complex128:
		return fmt.Errorf("complex number is not a JSON number: %w", ErrInvalidJSON)
	case reflect.Map, reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Map && value.IsNil() || value.Kind() == reflect.Slice && value.IsNil() {
			return nil
		}
		if value.Kind() != reflect.Array {
			visit := containerVisit{typeOf: value.Type(), ptr: uintptr(value.UnsafePointer())}
			if visiting[visit] {
				return fmt.Errorf("cyclic JSON value: %w", ErrInvalidJSON)
			}
			visiting[visit] = true
			defer delete(visiting, visit)
		}
		if value.Kind() == reflect.Map {
			iterator := value.MapRange()
			for iterator.Next() {
				if err := validateNumbers(iterator.Value(), visiting); err != nil {
					return err
				}
			}
			return nil
		}
		for index := 0; index < value.Len(); index++ {
			if err := validateNumbers(value.Index(index), visiting); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFloat(value float64) error {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return fmt.Errorf("number is not finite binary64: %w", ErrInvalidJSON)
	}
	if math.Trunc(value) == value && math.Abs(value) > float64(maxSafeJSONInteger) {
		return fmt.Errorf("integer is outside the safe JSON range: %w", ErrInvalidJSON)
	}
	return nil
}

func validateJSONNumber(raw string) error {
	if raw == "" || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !json.Valid([]byte(raw)) {
		return fmt.Errorf("invalid JSON number: %w", ErrInvalidJSON)
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		var parseErr *strconv.NumError
		if !errors.As(err, &parseErr) || !errors.Is(parseErr.Err, strconv.ErrRange) {
			return fmt.Errorf("invalid JSON number: %w", ErrInvalidJSON)
		}
	}
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return fmt.Errorf("number is not finite binary64: %w", ErrInvalidJSON)
	}
	if value == 0 && !isZeroJSONNumber(raw) {
		return fmt.Errorf("nonzero JSON number underflows binary64: %w", ErrInvalidJSON)
	}
	return validateFloat(value)
}

func isZeroJSONNumber(raw string) bool {
	for _, char := range raw {
		if char == 'e' || char == 'E' {
			return true
		}
		if char >= '1' && char <= '9' {
			return false
		}
	}
	return true
}

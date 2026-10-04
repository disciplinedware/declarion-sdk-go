package jsondoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
)

var (
	ErrLimit       = errors.New("JSON structural budget exceeded")
	ErrInvalidJSON = errors.New("invalid JSON structure")
	interfaceSize  = uint64(reflect.TypeOf((*any)(nil)).Elem().Size())
	mapSize        = uint64(reflect.TypeOf(map[string]any{}).Size())
	sliceSize      = uint64(reflect.TypeOf([]any{}).Size())
	stringSize     = uint64(reflect.TypeOf("").Size())
	boolSize       = uint64(reflect.TypeOf(false).Size())
	floatSize      = uint64(reflect.TypeOf(float64(0)).Size())
	wordSize       = uint64(reflect.TypeOf(uintptr(0)).Size())
)

type frame struct {
	kind      json.Delim
	expectKey bool
}

// ValidateDecodedBudget bounds a structural estimate before JSON aggregation.
func ValidateDecodedBudget(raw []byte, decodedMemoryBytes int64) error {
	if decodedMemoryBytes <= 0 {
		return fmt.Errorf("decoded-memory limit must be positive: %w", ErrLimit)
	}
	if int64(len(raw)) > decodedMemoryBytes {
		return fmt.Errorf("JSON input is %d bytes; decoded-memory budget is %d: %w", len(raw), decodedMemoryBytes, ErrLimit)
	}
	_, err := measureDecodedMemory(raw, decodedMemoryBytes)
	return err
}

func MeasureDecodedMemory(raw []byte) (int64, error) {
	return measureDecodedMemory(raw, math.MaxInt64)
}

func measureDecodedMemory(raw []byte, decodedMemoryBytes int64) (int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var stack []frame
	var used int64
	rootStarted, rootComplete := false, false
	charge := func(size uint64) error {
		if size > uint64(decodedMemoryBytes) || used > decodedMemoryBytes-int64(size) {
			return fmt.Errorf("decoded JSON exceeds %d-byte structural budget: %w", decodedMemoryBytes, ErrLimit)
		}
		used += int64(size)
		return nil
	}
	consumeValue := func() {
		if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
			stack[len(stack)-1].expectKey = true
		}
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("decode JSON structure: %w: %v", ErrInvalidJSON, err)
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				if len(stack) == 0 {
					if rootStarted {
						return 0, fmt.Errorf("multiple JSON values: %w", ErrInvalidJSON)
					}
					rootStarted = true
				} else {
					if err := charge(interfaceSize); err != nil {
						return 0, err
					}
					consumeValue()
				}
				headerSize := sliceSize
				if delim == '{' {
					headerSize = mapSize
				}
				if err := charge(headerSize); err != nil {
					return 0, err
				}
				stack = append(stack, frame{kind: delim, expectKey: delim == '{'})
			case '}', ']':
				if len(stack) == 0 || (delim == '}' && stack[len(stack)-1].kind != '{') || (delim == ']' && stack[len(stack)-1].kind != '[') {
					return 0, fmt.Errorf("unbalanced JSON container: %w", ErrInvalidJSON)
				}
				if stack[len(stack)-1].kind == '{' && !stack[len(stack)-1].expectKey {
					return 0, fmt.Errorf("incomplete JSON object member: %w", ErrInvalidJSON)
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					rootComplete = true
				}
			default:
				return 0, fmt.Errorf("unexpected JSON delimiter: %w", ErrInvalidJSON)
			}
			continue
		}
		if len(stack) > 0 && stack[len(stack)-1].kind == '{' && stack[len(stack)-1].expectKey {
			key, ok := token.(string)
			if !ok {
				return 0, fmt.Errorf("JSON object key is not a string: %w", ErrInvalidJSON)
			}
			entrySize := stringSize + interfaceSize + wordSize + uint64(len(key))
			if err := charge(entrySize); err != nil {
				return 0, err
			}
			stack[len(stack)-1].expectKey = false
			continue
		}
		if len(stack) == 0 {
			if rootStarted {
				return 0, fmt.Errorf("multiple JSON values: %w", ErrInvalidJSON)
			}
			rootStarted, rootComplete = true, true
		} else {
			if stack[len(stack)-1].kind == '[' {
				if err := charge(interfaceSize * 2); err != nil {
					return 0, err
				}
			}
			consumeValue()
		}
		size := interfaceSize
		switch value := token.(type) {
		case string:
			size += stringSize + uint64(len(value))
		case json.Number:
			size += floatSize
		case bool:
			size += boolSize
		case nil:
		default:
			return 0, fmt.Errorf("unsupported JSON token: %w", ErrInvalidJSON)
		}
		if err := charge(size); err != nil {
			return 0, err
		}
	}
	if !rootStarted || !rootComplete || len(stack) != 0 {
		return 0, fmt.Errorf("incomplete JSON value: %w", ErrInvalidJSON)
	}
	return used, nil
}

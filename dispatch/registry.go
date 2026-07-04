package dispatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/disciplinedware/declarion-sdk-go/handlerparam"
)

var ErrNotFound = errors.New("handler not found")

type DecodeError struct {
	Err error
}

func (e *DecodeError) Error() string { return "invalid params: " + e.Err.Error() }
func (e *DecodeError) Unwrap() error { return e.Err }

type Declaration struct {
	Code   string
	Params handlerparam.Params
}

type handler[C any] struct {
	code   string
	params handlerparam.Params
	exec   func(C, json.RawMessage) (json.RawMessage, error)
}

type Registry[C any] struct {
	mu       sync.RWMutex
	handlers map[string]handler[C]
	order    []string
}

func NewRegistry[C any]() *Registry[C] {
	return &Registry[C]{handlers: make(map[string]handler[C])}
}

func RegisterHandler[C, P, R any](reg *Registry[C], code string, fn func(C, P) (R, error)) {
	if reg == nil {
		panic("dispatch.RegisterHandler: nil registry")
	}
	if code == "" {
		panic("dispatch.RegisterHandler: code must be non-empty")
	}
	params := handlerparam.ReflectParams[P]()
	h := handler[C]{
		code:   code,
		params: params,
		exec: func(ctx C, raw json.RawMessage) (json.RawMessage, error) {
			var p P
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, &DecodeError{Err: err}
				}
			}
			result, err := fn(ctx, p)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return nil, fmt.Errorf("marshal handler result: %w", err)
			}
			return encoded, nil
		},
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, exists := reg.handlers[code]; exists {
		panic(fmt.Sprintf("dispatch.RegisterHandler: duplicate handler %q", code))
	}
	reg.handlers[code] = h
	reg.order = append(reg.order, code)
}

func (r *Registry[C]) Execute(code string, ctx C, params json.RawMessage) (json.RawMessage, error) {
	if r == nil {
		return nil, ErrNotFound
	}
	r.mu.RLock()
	h, ok := r.handlers[code]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	return h.exec(ctx, params)
}

func (r *Registry[C]) Declarations() []Declaration {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Declaration, 0, len(r.order))
	for _, code := range r.order {
		h := r.handlers[code]
		params := make(handlerparam.Params, len(h.params))
		copy(params, h.params)
		out = append(out, Declaration{Code: code, Params: params})
	}
	return out
}

func (r *Registry[C]) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.handlers)
}

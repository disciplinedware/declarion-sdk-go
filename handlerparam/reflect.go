package handlerparam

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

var stringBackedTypeTags = map[string]bool{
	"password": true,
	"text":     true,
	"url":      true,
	"email":    true,
	"phone":    true,
}

// ReflectParams derives the minimal handler-param wire declarations from P's
// exported Go struct fields. Param order follows struct field order.
func ReflectParams[P any]() Params {
	var zero P
	t := reflect.TypeOf(zero)
	return reflectParams(t)
}

func reflectParams(t reflect.Type) Params {
	if t == nil {
		panic("handler params must be a struct, got <nil>")
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("handler params must be a struct, got %s", t.Kind()))
	}

	seen := map[string]bool{}
	out := make(Params, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		code := strings.Split(jsonTag, ",")[0]
		if code == "" {
			continue
		}
		if seen[code] {
			panic(fmt.Sprintf("handler param %q: duplicate json tag", code))
		}
		seen[code] = true

		param := Param{Name: code}
		if v := field.Tag.Get("type"); v != "" {
			if !typeTagCompatible(v, field.Type) {
				panic(fmt.Sprintf("handler param %q: type:%q is incompatible with Go field kind %s", code, v, field.Type.Kind()))
			}
			param.Type = v
		} else {
			param.Type = goTypeToParamType(field.Type)
		}
		if field.Tag.Get("required") == "true" {
			param.Required = true
		}
		if field.Tag.Get("hidden") == "true" {
			param.Hidden = true
		}
		if field.Tag.Get("sensitive") == "true" {
			param.Sensitive = true
		}
		if v := field.Tag.Get("default"); v != "" {
			param.Default = v
		}
		if v := field.Tag.Get("enum"); v != "" {
			param.Enum = v
		}
		out = append(out, param)
	}
	return out
}

func typeTagCompatible(typeTag string, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if stringBackedTypeTags[typeTag] {
		return t.Kind() == reflect.String
	}
	return true
}

func goTypeToParamType(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return "timestamp"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Bool:
		return "bool"
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Struct, reflect.Interface:
		return "json"
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128:
		panic(fmt.Sprintf("unsupported handler param Go kind %s", t.Kind()))
	default:
		panic(fmt.Sprintf("unsupported handler param Go kind %s", t.Kind()))
	}
}

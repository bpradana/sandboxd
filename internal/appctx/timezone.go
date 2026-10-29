package appctx

import (
	"encoding/json"
	"reflect"
	"time"
)

func normalizeTimeUTC(value any) any {
	if value == nil {
		return nil
	}
	converted := convertValue(reflect.ValueOf(value))
	if !converted.IsValid() {
		return nil
	}
	return converted.Interface()
}

func convertValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	if value.CanInterface() {
		if t, ok := value.Interface().(time.Time); ok {
			return reflect.ValueOf(t.UTC())
		}
		if t, ok := value.Interface().(*time.Time); ok {
			if t == nil {
				return value
			}
			return reflect.ValueOf(new(t.UTC()))
		}
		if _, ok := value.Interface().(json.Marshaler); ok {
			return value
		}
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return value
		}
		return convertValue(value.Elem())
	case reflect.Pointer:
		if value.IsNil() {
			return value
		}
		converted := convertValue(value.Elem())
		ptr := reflect.New(value.Type().Elem())
		ptr.Elem().Set(converted)
		return ptr
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if !out.Field(i).CanSet() {
				continue
			}
			out.Field(i).Set(convertValue(value.Field(i)))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return value
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(convertValue(value.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(convertValue(value.Index(i)))
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return value
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		for _, key := range value.MapKeys() {
			convertedKey := convertValue(key)
			convertedValue := convertValue(value.MapIndex(key))
			out.SetMapIndex(convertedKey, convertedValue)
		}
		return out
	default:
		return value
	}
}

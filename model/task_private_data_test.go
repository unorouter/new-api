package model

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TaskPrivateData.Value() decides "is this struct empty?" with a hand-written
// field list. A field added to the struct but not to that list makes the whole
// payload serialize to NULL whenever it is the only field set, silently losing
// the data (this happened to Execution during the 2026-09 upstream sync, which
// broke plugin artifact serving). Setting each field on its own must always
// produce a non-NULL value.
func TestTaskPrivateDataValuePersistsEveryField(t *testing.T) {
	structType := reflect.TypeOf(TaskPrivateData{})
	for i := range structType.NumField() {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			data := TaskPrivateData{}
			target := reflect.ValueOf(&data).Elem().Field(i)
			switch target.Kind() {
			case reflect.String:
				target.SetString("x")
			case reflect.Int, reflect.Int64:
				target.SetInt(1)
			case reflect.Bool:
				target.SetBool(true)
			case reflect.Slice:
				target.Set(reflect.MakeSlice(target.Type(), 1, 1))
			case reflect.Ptr:
				target.Set(reflect.New(target.Type().Elem()))
			case reflect.Map:
				target.Set(reflect.MakeMap(target.Type()))
			default:
				t.Skipf("unhandled kind %s", target.Kind())
			}

			value, err := data.Value()
			require.NoError(t, err)
			assert.NotNilf(t, value,
				"TaskPrivateData.Value() returned NULL with only %s set: add it to the zero-check in Value()", field.Name)
		})
	}
}

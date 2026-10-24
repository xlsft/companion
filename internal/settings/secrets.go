package settings

import (
	"reflect"

	"github.com/xlsft/openclick/pkg/contracts"
)

var secretRefType = reflect.TypeOf(contracts.SecretRef(""))

func isSecretField(f reflect.StructField) bool {
	return f.Type == secretRefType || f.Tag.Get("secret") == "true"
}

// walkSecrets calls fn for every secret field of the struct pointed to by v,
// recursing into nested structs. a and b are parallel walks (b may be the
// zero Value when only one struct is involved).
func walkSecrets(a, b reflect.Value, fn func(a, b reflect.Value)) {
	t := a.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		af := a.Field(i)
		var bf reflect.Value
		if b.IsValid() {
			bf = b.Field(i)
		}
		switch {
		case isSecretField(f):
			fn(af, bf)
		case f.Type.Kind() == reflect.Struct:
			walkSecrets(af, bf, fn)
		}
	}
}

// stripSecrets zeroes every secret field in *v. Secret fields must carry
// `omitempty` so they vanish from the exported JSON instead of showing "".
func stripSecrets(v any) {
	walkSecrets(reflect.ValueOf(v).Elem(), reflect.Value{}, func(a, _ reflect.Value) {
		a.Set(reflect.Zero(a.Type()))
	})
}

// restoreSecrets copies secret fields from src into dst (both pointers to
// the same struct type), so imported values can never overwrite them.
func restoreSecrets(dst, src any) {
	walkSecrets(reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem(), func(a, b reflect.Value) {
		a.Set(b)
	})
}

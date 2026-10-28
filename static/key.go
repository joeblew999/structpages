//go:build !tinygo

package static

import "reflect"

// Key identifies fn's code: the same for every value of one function,
// method expression or method value, whatever its receiver.
func Key(fn reflect.Value) uintptr { return fn.Pointer() }

// KeyOf is Key(reflect.ValueOf(fn)), fn a function value.
func KeyOf(fn any) uintptr { return reflect.ValueOf(fn).Pointer() }

//go:build tinygo

package static

import (
	"reflect"
	"unsafe"
)

// Key identifies fn's code: the same for every value of one function,
// method expression or method value, whatever its receiver. A TinyGo
// function value is two words, a context and the code, and Value.Pointer
// gives the context when there is one (a method value's receiver), so Key
// reads the code from a copy.
func Key(fn reflect.Value) uintptr {
	c := reflect.New(fn.Type())
	c.Elem().Set(fn)
	return uintptr((*[2]unsafe.Pointer)(c.UnsafePointer())[1])
}

// KeyOf is Key(reflect.ValueOf(fn)), fn a function value, without the copy:
// an interface holds a function value boxed, so its data word points at the
// two words, and the code is the second.
func KeyOf(fn any) uintptr {
	type eface struct{ typ, data unsafe.Pointer }
	return uintptr((*[2]unsafe.Pointer)((*eface)(unsafe.Pointer(&fn)).data)[1])
}

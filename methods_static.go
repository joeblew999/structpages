//go:build tinygo || structpages_static

package structpages

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jackielii/structpages/static"
)

// What structpages reads about functions and methods comes from the code
// structpages-static generate writes (package static), because TinyGo can't: its reflect
// panics as unimplemented in Type.Method, MethodByName, In, Out and
// Value.Call (https://github.com/tinygo-org/tinygo/issues/3862), and its
// runtime.FuncForPC returns nil
// (https://github.com/tinygo-org/tinygo/issues/4379). methods_dynamic.go
// reads the same from reflect elsewhere; the structpages_static tag builds
// this file on the standard toolchain, to test it there.
//
// When a TinyGo release has all of those, delete this file,
// methods_static_test.go, cmpdiff_tinygo_test.go, package generated and
// tools/static, and drop the build constraints from
// methods_dynamic.go, methods_dynamic_test.go and cmpdiff_test.go (and the
// staticBuild checks with them). It is safe when the suite passes under
// `tinygo test` with no generated code.

// funcTable is t's generated entry. Mount checks every type it will call
// (checkFuncType, typeMethods), so a missing one here is a bug.
func funcTable(t reflect.Type) *static.FuncType {
	ft, ok := static.LookupFuncType(t)
	if !ok {
		panic(fmt.Sprintf("structpages: no generated code for function type %s; run go tool structpages-static generate", t))
	}
	return ft
}

func funcNumIn(t reflect.Type) int               { return len(funcTable(t).In) }
func funcIn(t reflect.Type, i int) reflect.Type  { return funcTable(t).In[i] }
func funcNumOut(t reflect.Type) int              { return len(funcTable(t).Out) }
func funcOut(t reflect.Type, i int) reflect.Type { return funcTable(t).Out[i] }
func funcIsVariadic(t reflect.Type) bool         { return funcTable(t).Variadic }

// funcCall calls fn with in, as fn.Call does.
func funcCall(fn reflect.Value, in []reflect.Value) []reflect.Value {
	return funcTable(fn.Type()).Call(fn, in)
}

// checkFuncType reports whether funcIn, funcOut and funcCall work for
// functions of type t: whether structpages-static generate wrote code for it.
func checkFuncType(t reflect.Type) error {
	if _, ok := static.LookupFuncType(t); !ok {
		return fmt.Errorf("no generated code for function type %s; run go tool structpages-static generate", t)
	}
	return nil
}

// funcKey identifies fn's code, fn a function value: the same for every
// value of one function, method expression or method value, whatever its
// receiver. Under TinyGo it reads the code from the interface, without the
// copy static.Key makes, which would cost every ID an allocation.
func funcKey(fn any) uintptr { return static.KeyOf(fn) }

// funcName is the runtime's name for fn's code, such as
// "example.com/app.(*page).Content-fm", or "" when no generated code has it.
func funcName(fn reflect.Value) string { return static.Name(fn) }

// typeMethods lists t's exported methods, as t.Method does.
func typeMethods(t reflect.Type) ([]reflect.Method, error) {
	ms, ok := static.Methods(t)
	if !ok {
		if embedded, ok := embeddedMethods(t); ok {
			return embedded, nil
		}
		return nil, fmt.Errorf("no generated methods for %s; run go tool structpages-static generate", t)
	}
	for _, m := range ms {
		// structpages never calls a promoted method, and the generator
		// can't always write its type.
		if !isPromotedMethod(&m) {
			if err := checkFuncType(m.Type); err != nil {
				return nil, fmt.Errorf("method %s.%s: %w", t, m.Name, err)
			}
		}
	}
	return ms, nil
}

// embeddedMethods lists the methods of t, a struct type or a pointer to one
// without generated code, when they all come from its embedded fields: a struct
// declared inside a function, which can't declare methods and which the
// generated code can't name, such as a root embedding its pages. They are
// promoted, so structpages only ever looks them up by name, and they carry
// no Func.
func embeddedMethods(t reflect.Type) ([]reflect.Method, bool) {
	if t.NumMethod() == 0 {
		return nil, true
	}
	st, ptr := t, t.Kind() == reflect.Pointer
	if ptr {
		st = t.Elem()
	}
	if st.Kind() != reflect.Struct {
		return nil, false
	}
	// A name more than one embedded field has is ambiguous, so not
	// promoted (without telling depths apart, which reflect would).
	seen := map[string]int{}
	var all []reflect.Method
	for i := range st.NumField() {
		f := st.Field(i)
		if !f.Anonymous {
			continue
		}
		// S has E's value methods; *S, and S embedding *E, all of *E's.
		ft := f.Type
		if ptr && ft.Kind() != reflect.Pointer {
			ft = reflect.PointerTo(ft)
		}
		fms, err := typeMethods(ft)
		if err != nil {
			return nil, false
		}
		for _, m := range fms {
			seen[m.Name]++
			if seen[m.Name] == 1 {
				all = append(all, reflect.Method{Name: m.Name, Type: m.Type})
			}
		}
	}
	if t.NumMethod() > len(all) {
		return nil, false // t declares methods of its own: it needs generated code
	}
	var ms []reflect.Method
	for _, m := range all {
		if seen[m.Name] == 1 {
			ms = append(ms, m)
		}
	}
	slices.SortFunc(ms, func(a, b reflect.Method) int { return strings.Compare(a.Name, b.Name) })
	for i := range ms {
		ms[i].Index = i
	}
	return ms, true
}

// methodByName finds t's exported method name, as t.MethodByName does.
func methodByName(t reflect.Type, name string) (reflect.Method, bool) {
	if m, ok := static.MethodByName(t, name); ok {
		return m, true
	}
	ms, _ := embeddedMethods(t)
	for _, m := range ms {
		if m.Name == name {
			return m, true
		}
	}
	return reflect.Method{}, false
}

// isPromotedMethod reports what the runtime's autogenerated wrappers would
// (see methods_dynamic.go). Methods listed from embedded fields alone carry
// no Func and are all promoted.
func isPromotedMethod(method *reflect.Method) bool {
	return !method.Func.IsValid() || static.IsPromoted(method.Func)
}

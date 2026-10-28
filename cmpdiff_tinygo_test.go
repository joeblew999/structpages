//go:build tinygo

package structpages

import (
	"fmt"
	"reflect"
)

// cmpDiff reports the differences between x and y, or "" when they are
// equal. go-cmp looks for Equal methods with reflect's MethodByName, which
// TinyGo lacks, so TinyGo compares with reflect.DeepEqual (see
// reflect_generated.go for when this file can go).
func cmpDiff(x, y any) string {
	if reflect.DeepEqual(x, y) {
		return ""
	}
	return fmt.Sprintf("-%#v\n+%#v", x, y)
}

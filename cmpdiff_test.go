//go:build !tinygo

package structpages

import "github.com/google/go-cmp/cmp"

// cmpDiff reports the differences between x and y as cmp.Diff does, or ""
// when they are equal.
func cmpDiff(x, y any) string { return cmp.Diff(x, y) }

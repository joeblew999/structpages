//go:build tinygo || structpages_static

package structpages

// staticBuild reports whether structpages reads methods from generated
// code (methods_static.go) instead of reflect.
const staticBuild = true

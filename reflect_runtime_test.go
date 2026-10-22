//go:build !tinygo && !structpages_tables

package structpages

// tablesBuild reports whether structpages reads methods from generated
// tables (reflect_tables.go) instead of reflect.
const tablesBuild = false

package config

import _ "embed"

// example is a copy of config.example.toml at the repository root, which
// embedding cannot reach; a test keeps the two the same.
//
//go:embed example.toml
var example string

// Example returns the annotated example configuration.
func Example() string { return example }

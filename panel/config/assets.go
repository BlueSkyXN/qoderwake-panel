// Package contracts contains the versioned official CLI reference and policy.
package contracts

import _ "embed"

//go:embed cli-reference.json
var Reference []byte

//go:embed cli-policy.json
var Policy []byte

//go:embed whitelist.example.json
var Whitelist []byte

//go:embed patch-registry.json
var Patches []byte

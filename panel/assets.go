// Package legacycontracts contains the frozen Linux gateway helper contracts.
package legacycontracts

import "embed"

//go:embed gateway_policy.py gateway_runtime.py
var Files embed.FS

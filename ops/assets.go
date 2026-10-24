// Package legacyops contains the existing Linux gateway lifecycle helper.
package legacyops

import "embed"

//go:embed gateway-manager.py uplink-gw.py
var Files embed.FS

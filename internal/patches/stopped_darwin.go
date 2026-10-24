//go:build darwin

package patches

import (
	"context"
	"errors"
)

func requireStopped(context.Context, string) error { return errors.New("patch_platform_unsupported") }

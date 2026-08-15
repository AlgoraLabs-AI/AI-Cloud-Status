//go:build !windows

package ui

import "context"

// runDeadCanvasWatchdog is Windows-only: the dead-GL-surface failure has only
// been observed there (Intel iGPU + DisplayLink virtual displays) and the
// pixel-probe detection is Win32-based. Elsewhere it does nothing.
func (c *Controller) runDeadCanvasWatchdog(ctx context.Context) {}

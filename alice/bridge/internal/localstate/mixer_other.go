//go:build !linux

package localstate

import "context"

func mixerEvents(context.Context) <-chan struct{} { return nil }

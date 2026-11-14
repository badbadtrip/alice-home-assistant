//go:build !linux || (!arm && !arm64 && !amd64)

package pcm

import (
	"context"
	"errors"
	"io"
)

func Check() error                                  { return errors.New("PCM output requires Linux arm, arm64 or amd64") }
func Play(context.Context, io.Reader, Config) error { return Check() }

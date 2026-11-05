//go:build !linux

package indicator

import "errors"

type ring struct{ size int }

func openRing() (*ring, error) { return nil, errors.New("LED ring needs Linux") }

func (*ring) show([]Pixel) error { return nil }

//go:build !unix

package main

import (
	"errors"
	"os"
)

func interruptibleInput(*os.File) (*os.File, error) {
	return nil, errors.New("PCM input requires Unix")
}

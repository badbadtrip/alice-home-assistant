// Package pcmpackage builds and embeds owned PCM payloads into one executable.
package pcmpackage

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/skrashevich/station-airplay/internal/pcmbundle"
)

type Options struct {
	Arch        string
	Binary      string
	Output      string
	Armv7Binary string
	Arm64Binary string
	UPX         bool
}

// Build copies the input executable, builds or reads PCM helpers, and appends
// their bundle before atomically publishing one output file. Empty Output means
// replace Binary in place. Tests, installation and firmware changes are callers' work.
func Build(ctx context.Context, options Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Arch != "armv7" && options.Arch != "arm64" {
		return fmt.Errorf("unsupported package architecture %q", options.Arch)
	}
	if options.Binary == "" {
		return fmt.Errorf("input executable is required")
	}
	if err := validateELF(options.Binary, options.Arch); err != nil {
		return err
	}
	input, err := os.Open(options.Binary)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output := options.Output
	if output == "" {
		output = options.Binary
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(output), ".station-pcm-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	executable := filepath.Join(stage, "receiver")
	copied, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(copied, input)
	inputCloseErr := input.Close()
	closeErr := copied.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if inputCloseErr != nil {
		return inputCloseErr
	}
	if err := os.Chmod(executable, info.Mode().Perm()); err != nil {
		return err
	}
	targets := []string{"armv7"}
	if options.Arch == "arm64" {
		targets = append(targets, "arm64")
	}
	helpers := make(map[string][]byte, len(targets))
	for _, arch := range targets {
		path := options.Armv7Binary
		goarch, loader := "arm", "/lib/ld-linux-armhf.so.3"
		if arch == "arm64" {
			path, goarch, loader = options.Arm64Binary, "arm64", "/lib/ld-linux-aarch64.so.1"
		}
		if path == "" {
			path = filepath.Join(stage, "station-pcm-"+arch)
			// Full import path also resolves from an embedding application's vendor tree.
			command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags=-s -w -I "+loader, "-o", path, "github.com/skrashevich/station-airplay/cmd/station-pcm")
			command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+goarch, "GOARM=7")
			command.Stdout, command.Stderr = os.Stderr, os.Stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("build %s helper: %w", arch, err)
			}
		}
		if err := validateELF(path, arch); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		helpers[arch] = data
	}
	if options.UPX {
		// UPX must only see the host ELF. Compressing an existing ZIP overlay
		// adds UPX metadata after it, which cannot safely be discarded on rebundle.
		if _, err := pcmbundle.Strip(ctx, executable); err != nil {
			return fmt.Errorf("remove PCM payload before compression: %w", err)
		}
		for _, args := range [][]string{{"--ultra-brute", "--no-lzma", executable}, {"-t", executable}} {
			command := exec.CommandContext(ctx, "upx", args...)
			command.Stdout, command.Stderr = os.Stderr, os.Stderr
			if err := command.Run(); err != nil {
				return fmt.Errorf("UPX %s: %w (use an unpacked input with -upx)", args[0], err)
			}
		}
	}
	if err := pcmbundle.Append(ctx, executable, helpers); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(executable, output)
}

func validateELF(path, arch string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	header := make([]byte, 20)
	_, err = io.ReadFull(file, header)
	if err != nil || string(header[:4]) != "\x7fELF" || header[5] != 1 {
		return fmt.Errorf("%s is not a little-endian Linux ELF", filepath.Base(path))
	}
	class, machine := byte(1), uint16(40)
	if arch == "arm64" {
		class, machine = 2, 183
	}
	if header[4] != class || binary.LittleEndian.Uint16(header[18:]) != machine {
		return fmt.Errorf("%s has a different ELF architecture than %s", filepath.Base(path), arch)
	}
	return nil
}

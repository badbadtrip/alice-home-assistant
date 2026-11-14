// station-pcm accepts the narrow raw-PCM command line used by station-airplay.
// It runs separately so ALSA's dynamic loader cannot affect the static receiver or its embedding host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/skrashevich/station-airplay/internal/pcm"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	play := func(ctx context.Context, _ io.Reader, cfg pcm.Config) error {
		// Only playback owns a pipe. Preflight, help and invalid arguments must not
		// change an inherited shell's input flags by setting its duplicate nonblocking.
		input, err := interruptibleInput(os.Stdin)
		if err != nil {
			return err
		}
		defer input.Close()
		cancelRead := context.AfterFunc(ctx, func() { input.Close() })
		defer cancelRead()
		return pcm.Play(ctx, input, cfg)
	}
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stderr, pcm.Check, play); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "station-pcm:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer, check func() error, play func(context.Context, io.Reader, pcm.Config) error) error {
	flags := flag.NewFlagSet("station-pcm", flag.ContinueOnError)
	flags.SetOutput(output)
	cfg := pcm.Config{}
	flags.StringVar(&cfg.Device, "D", "default", "ALSA PCM name")
	flags.IntVar(&cfg.Rate, "r", 44100, "PCM sample rate (44100 or 48000)")
	format := flags.String("f", "S16_LE", "sample format (S16_LE)")
	channels := flags.Int("c", 2, "channels (2)")
	fileType := flags.String("t", "raw", "input format (raw)")
	flags.Bool("q", false, "accepted for compatibility; output is always quiet")
	checkOnly := flags.Bool("check", false, "load ALSA and check symbols without opening a PCM")
	version := flags.Bool("version", false, "print helper version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("PCM is read from stdin; filenames are unsupported")
	}
	if *format != "S16_LE" || *channels != 2 || *fileType != "raw" || cfg.Rate != 44100 && cfg.Rate != 48000 {
		return errors.New("only raw stereo S16_LE at 44100 or 48000 Hz is supported")
	}
	if *version {
		_, err := fmt.Fprintln(output, "station-pcm (ALSA stereo S16_LE output)")
		return err
	}
	if *checkOnly {
		return check()
	}
	return play(ctx, input, cfg)
}

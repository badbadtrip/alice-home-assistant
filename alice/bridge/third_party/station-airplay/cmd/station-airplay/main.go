package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	airplay "github.com/skrashevich/station-airplay"
	"github.com/skrashevich/station-airplay/internal/aliceipc"
)

var version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	var cfg airplay.Config
	flag.StringVar(&cfg.Name, "name", "", "Name shown in AirPlay (default: hostname + AirPlay)")
	flag.StringVar(&cfg.Interface, "interface", "", "Network interface (automatic by default)")
	flag.StringVar(&cfg.ListenAddress, "listen", ":7000", "IPv4 RTSP address and port")
	flag.StringVar(&cfg.DataDir, "data-dir", "/data/opt/station-airplay/state", "Persistent identity and pairing directory")
	flag.StringVar(&cfg.Aplay, "aplay", "", "Custom aplay-compatible output (default: bundled station-pcm)")
	flag.StringVar(&cfg.Device, "device", "default", "ALSA playback PCM")
	cfg.OutputDelay = flag.Duration("output-delay", 160*time.Millisecond, "Estimated audio output latency")
	cfg.InitialVolumeDB = flag.Float64("volume", -20, "Initial AirPlay volume in dB")
	check := flag.Bool("check", false, "Check prerequisites without opening listeners or audio")
	showVersion := flag.Bool("version", false, "Print build version")
	voiceAddress := flag.String("alice-ipc", "127.0.0.1:9875", "Local Alice IPC address; empty disables voice media commands")
	voiceCancel := flag.String("alice-cancel", "dialog", "Experimental cancellation: dialog or clear (dialog and queue)")
	flag.Parse()
	if *showVersion {
		fmt.Printf("station-airplay %s (pure Go receiver, bundled PCM output)\n", version)
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, cfg, *check, *voiceAddress, *voiceCancel); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg airplay.Config, check bool, voiceAddress, voiceCancel string) error {
	method := 1
	switch voiceCancel {
	case "dialog":
	case "clear":
		method = 6
	default:
		return fmt.Errorf("alice-cancel must be dialog or clear")
	}
	receiver, err := airplay.New(cfg)
	if err != nil {
		return err
	}
	if check {
		result, err := receiver.Check(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("version=%s interface=%s ip=%s aplay=%s pcm=%s state=%s\n",
			version, result.Interface, result.IP, result.Aplay, result.Device, result.DataDir)
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := receiver.Start(ctx); err != nil {
		return err
	}
	voiceDone := make(chan error, 1)
	if voiceAddress == "" {
		voiceDone <- nil
	} else {
		go func() {
			err := aliceipc.Run(ctx, aliceipc.Config{Address: voiceAddress, CancelMethod: method, Logger: log.Default()}, func() bool {
				status := receiver.Status()
				return status.State == airplay.StateRunning && status.Connected
			}, receiver.Control)
			voiceDone <- err
			cancel()
		}()
		go logPlayback(ctx, receiver)
	}
	// Wait with an independent context: SIGTERM must complete cleanup before exit.
	err = receiver.Wait(context.Background())
	cancel()
	return errors.Join(err, <-voiceDone)
}

// Prototype diagnostics record playback transitions, never recognized speech.
func logPlayback(ctx context.Context, receiver *airplay.Receiver) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var previous airplay.Status
	for {
		status := receiver.Status()
		if status.State != previous.State || status.Connected != previous.Connected || status.Playing != previous.Playing {
			log.Printf("alice_airplay_state state=%s connected=%t playing=%t", status.State, status.Connected, status.Playing)
			previous = status
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

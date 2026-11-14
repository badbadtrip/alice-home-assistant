package main

import (
	"context"
	"flag"
	"github.com/skrashevich/station-airplay/internal/probe"
	"log"
)

func main() {
	address := flag.String("address", "127.0.0.1:7000", "Receiver address")
	file := flag.String("aac", "", "AAC-LC stereo ADTS fixture")
	volume := flag.Float64("volume", -35, "Test stream volume in dB")
	flag.Parse()
	if *file == "" {
		log.Fatal("-aac is required")
	}
	if err := probe.Run(context.Background(), *address, *file, *volume); err != nil {
		log.Fatal(err)
	}
	log.Print("synthetic pairing, encrypted control and AAC stream completed")
}

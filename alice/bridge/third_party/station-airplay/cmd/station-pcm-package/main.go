// station-pcm-package embeds PCM payloads into a single receiver executable.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/skrashevich/station-airplay/internal/pcmpackage"
)

func main() {
	var options pcmpackage.Options
	flag.StringVar(&options.Arch, "arch", "arm64", "Receiver package architecture: arm64 or armv7")
	flag.StringVar(&options.Binary, "binary", "", "Input receiver executable")
	flag.StringVar(&options.Output, "output", "", "Output executable; omit to replace -binary atomically")
	flag.BoolVar(&options.UPX, "upx", false, "Compress the unpacked host with UPX before embedding PCM payloads")
	flag.StringVar(&options.Armv7Binary, "armv7-binary", "", "Prebuilt ARMv7 helper; otherwise build it")
	flag.StringVar(&options.Arm64Binary, "arm64-binary", "", "Prebuilt ARM64 helper; otherwise build it")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "station-pcm-package: unexpected positional arguments")
		os.Exit(2)
	}
	if err := pcmpackage.Build(context.Background(), options); err != nil {
		fmt.Fprintln(os.Stderr, "station-pcm-package:", err)
		os.Exit(1)
	}
}

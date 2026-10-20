// qa-soak is an opt-in offline QA executable, never a Discord client.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	"whiterun/internal/qasoak"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "source" {
		if err := qasoak.Source(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(7)
		}
		return
	}
	var c qasoak.Config
	flag.StringVar(&c.Out, "out", "", "new, dedicated QA directory (required)")
	flag.DurationVar(&c.Duration, "duration", time.Minute, "real duration of this continuous segment (1h, 6h, 12h supported)")
	flag.DurationVar(&c.Sample, "sample", 5*time.Second, "resource sample interval")
	flag.DurationVar(&c.Audio, "audio", 100*time.Millisecond, "local source duration")
	flag.IntVar(&c.Playlist, "playlist", 123, "123 or 500 tracks per batch")
	flag.IntVar(&c.Guilds, "guilds", 2, "simulated guilds (1-8)")
	flag.Uint64Var(&c.Seed, "seed", 20261019, "reproducible fault placement")
	flag.BoolVar(&c.Warm, "warm", false, "resolve each URL twice and measure cache hits")
	flag.BoolVar(&c.Resume, "resume", false, "new segment recovering the owned QA database")
	flag.BoolVar(&c.Faults, "faults", false, "inject isolated source/transport/cancellation failures")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := qasoak.Run(ctx, c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

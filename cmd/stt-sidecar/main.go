// Command stt-sidecar hosts the local speech recognition runtime and speaks
// JSON-RPC over stdin/stdout with the core (docs/stt-protocol.md).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/xlsft/companion/internal/buildinfo"
	"github.com/xlsft/companion/internal/stt/engine"
	"github.com/xlsft/companion/internal/stt/engine/fake"
	"github.com/xlsft/companion/internal/stt/server"
	"github.com/xlsft/companion/internal/stt/vad"
)

func main() {
	names := engine.Names()
	hasSherpa, hasWhisper := slices.Contains(names, "sherpa"), slices.Contains(names, "whisper")
	defaultEngine := "fake"
	switch {
	case hasSherpa && hasWhisper:
		defaultEngine = "auto"
	case hasSherpa:
		defaultEngine = "sherpa"
	case hasWhisper:
		defaultEngine = "whisper"
	}
	vcfg := vad.DefaultConfig()
	var (
		showVersion     = flag.Bool("version", false, "print version and exit")
		engineName      = flag.String("engine", defaultEngine, "speech engine: fake, auto (sherpa for a model directory, whisper for a file) or a compiled-in engine "+fmt.Sprint(names))
		partialInterval = flag.Duration("partial-interval", 700*time.Millisecond, "new audio between rolling partial decodes")
		partialWindow   = flag.Duration("partial-window", 10*time.Second, "trailing audio decoded for each partial")
		maxUtterance    = flag.Duration("max-utterance", 5*time.Minute, "per-session audio limit")
		vadOn           = flag.Bool("vad", true, "detect speech; false treats all audio as speech")
		vadThreshold    = flag.Float64("vad-threshold-db", vcfg.ThresholdDB, "frame RMS in dBFS counted as speech")
		vadHangover     = flag.Duration("vad-hangover", vcfg.Hangover, "silence that ends a speech run")
		fakeDelay       = flag.Duration("fake-delay", 0, "fake engine: delay per decode")
		fakeExit        = flag.Bool("fake-exit-on-final", false, "fake engine: exit during the final decode (crash simulation)")
		fakeStreaming   = flag.Bool("fake-streaming", false, "fake engine: behave as a streaming model")
		logLevel        = flag.String("log-level", "info", "stderr log level")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "stt-sidecar: bad -log-level")
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	var factory engine.Factory
	switch *engineName {
	case "fake":
		factory = fake.Factory(fake.Options{Delay: *fakeDelay, ExitOnFinal: *fakeExit, Streaming: *fakeStreaming})
	case "auto":
		factory = engine.Auto("sherpa", "whisper")
	default:
		f, err := engine.Lookup(*engineName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stt-sidecar:", err)
			os.Exit(2)
		}
		factory = f
	}

	vcfg.ThresholdDB, vcfg.Hangover = *vadThreshold, *vadHangover

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := server.New(server.Config{
		EngineName:      *engineName,
		Factory:         factory,
		PartialInterval: *partialInterval,
		PartialWindow:   *partialWindow,
		MaxUtterance:    *maxUtterance,
		VAD:             vcfg,
		VADDisabled:     !*vadOn,
		Log:             log,
	})
	log.Info("sidecar started", "version", buildinfo.Version, "engine", *engineName)
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		log.Error("sidecar stopped", "err", err)
		os.Exit(1)
	}
}

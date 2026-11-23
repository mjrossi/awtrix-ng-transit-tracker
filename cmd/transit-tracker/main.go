// Command transit-tracker shows upcoming public transit departures on an
// AWTRIX NG clock.
//
//	transit-tracker run
//	transit-tracker preview --board metro
//	transit-tracker config validate
//	transit-tracker stops search carrismetropolitana cais sodre
//	transit-tracker systems show nyc-subway
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	// Embeds the IANA zone database so timezone settings work in minimal
	// images without tzdata.
	_ "time/tzdata"

	"github.com/urfave/cli/v3"
)

// version is the release version reported by --version. The Dockerfile's
// VERSION build argument overrides it.
var version = "0.1.0"

func main() {
	// os.Exit skips defers, so run's cleanup happens inside run.
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCommand().Run(ctx, os.Args); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:    "transit-tracker",
		Usage:   "show public transit departures on an AWTRIX NG clock",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "path to the TOML configuration file",
				Value:   "config.toml",
				Sources: cli.EnvVars("TRANSIT_CONFIG"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "json or text",
				Value:   "json",
				Sources: cli.EnvVars("TRANSIT_LOG_FORMAT"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "debug, info, warn or error",
				Value:   "info",
				Sources: cli.EnvVars("TRANSIT_LOG_LEVEL"),
			},
		},
		Commands: []*cli.Command{
			runCommand(),
			previewCommand(),
			configCommand(),
			stopsCommand(),
			systemsCommand(),
		},
	}
}

func newLogger(c *cli.Command) (*slog.Logger, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(c.String("log-level"))); err != nil {
		return nil, fmt.Errorf("log-level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(c.String("log-format")) {
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, opts)), nil
	default:
		return nil, fmt.Errorf("log-format: want json or text, got %q", c.String("log-format"))
	}
}

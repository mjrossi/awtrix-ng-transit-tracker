// Command urbanist-timetable shows upcoming public transit departures in a
// terminal or on an AWTRIX NG clock.
//
//	urbanist-timetable departures mta-subway 127N
//	urbanist-timetable agencies search lisbon
//	urbanist-timetable stops search carris-metropolitana cais sodre
//	urbanist-timetable systems show mta-subway
//	urbanist-timetable config validate
//	urbanist-timetable clock run
//	urbanist-timetable clock preview --board metro
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	// Embeds the IANA zone database so timezone settings work in minimal
	// images without tzdata.
	_ "time/tzdata"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

// version is the release version reported by --version. The Dockerfile's
// VERSION build argument overrides it.
var version = "0.7.0"

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
		printError(os.Stderr, err)
		return 1
	}
	return 0
}

func newRootCommand() *cli.Command {
	return withUsageHandling(&cli.Command{
		Name:    "urbanist-timetable",
		Usage:   "show public transit departures in a terminal or on a pixel clock",
		Version: version,
		Description: "Getting started:\n\n" +
			"   urbanist-timetable agencies search mta               # find your agency's system\n" +
			"   urbanist-timetable stops search mta-subway times sq  # find a stop ID in it\n" +
			"   urbanist-timetable departures mta-subway 127N        # its next departures\n" +
			"   urbanist-timetable config init                       # save stops to show by default",
		EnableShellCompletion:           true,
		ConfigureShellCompletionCommand: showCompletionCommand,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Aliases: []string{"c"},
				Usage:   "path to the TOML configuration file",
				Value:   config.DefaultPath(),
				Sources: cli.EnvVars("TIMETABLE_CONFIG"),
			},
			&cli.StringFlag{
				Name:    "cache-dir",
				Usage:   "where GTFS schedules are kept between runs, or off",
				Value:   defaultCacheDir(),
				Sources: cli.EnvVars("TIMETABLE_CACHE"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "json or text; text by default when stderr is a terminal",
				Value:   defaultLogFormat(),
				Sources: cli.EnvVars("TIMETABLE_LOG_FORMAT"),
				Validator: func(s string) error {
					if f := strings.ToLower(s); f != "json" && f != "text" {
						return fmt.Errorf("want json or text, got %q", s)
					}
					return nil
				},
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "debug, info, warn or error",
				Value:   "info",
				Sources: cli.EnvVars("TIMETABLE_LOG_LEVEL"),
				Validator: func(s string) error {
					var level slog.Level
					return level.UnmarshalText([]byte(s))
				},
			},
		},
		Commands: []*cli.Command{
			departuresCommand(),
			stopsCommand(),
			systemsCommand(),
			agenciesCommand(),
			configCommand(),
			cacheCommand(),
			clockCommand(),
		},
	})
}

// defaultCacheDir is the user's cache directory for this program, or off
// where there is none.
func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "off"
	}
	return filepath.Join(dir, "urbanist-timetable")
}

// defaultLogFormat is text for a person at a terminal and JSON for a log
// collector, such as Docker's.
func defaultLogFormat() string {
	if isTerminal(os.Stderr) {
		return "text"
	}
	return "json"
}

// providerOptions passes --cache-dir and the logger to the providers, so a
// slow schedule download says what it is doing.
func providerOptions(c *cli.Command) []providers.Option {
	return []providers.Option{providers.WithScheduleCache(cacheDir(c)), providers.WithLogger(newLogger(c))}
}

// cacheDir is --cache-dir, or empty when it is off.
func cacheDir(c *cli.Command) string {
	if dir := c.String("cache-dir"); dir != "off" {
		return dir
	}
	return ""
}

// newLogger builds the logger the global flags describe. Their validators
// have already rejected bad values, so a failed parse here cannot happen.
func newLogger(c *cli.Command) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(c.String("log-level")))
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(c.String("log-format"), "json") {
		return slog.New(slog.NewJSONHandler(c.Root().ErrWriter, opts))
	}
	return slog.New(slog.NewTextHandler(c.Root().ErrWriter, opts))
}

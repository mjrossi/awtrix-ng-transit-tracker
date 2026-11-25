// Command urbanist-timetable shows upcoming public transit departures in a
// terminal or on an AWTRIX NG clock.
//
//	urbanist-timetable departures nyc-subway 127N
//	urbanist-timetable stops search carrismetropolitana cais sodre
//	urbanist-timetable systems show nyc-subway
//	urbanist-timetable config validate
//	urbanist-timetable clock run
//	urbanist-timetable clock preview --board metro
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
)

// version is the release version reported by --version. The Dockerfile's
// VERSION build argument overrides it.
var version = "0.6.0"

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

// printError prints err and a hint. Errors can quote server responses, so
// control characters are removed first.
func printError(w io.Writer, err error) {
	fmt.Fprintf(w, "error: %s\n", termout.Clean(err.Error()))
	if h := hint(err); h != "" {
		fmt.Fprintln(w, h)
	}
}

// hint suggests the command that fixes err, or returns "". The library's
// errors do not name commands, since programs other than this one use it.
func hint(err error) string {
	var missing *catalog.MissingOptionsError
	switch {
	case errors.As(err, &missing):
		return "hint: run `urbanist-timetable systems show " + missing.System + "`"
	case errors.Is(err, catalog.ErrUnknownSystem):
		return "hint: run `urbanist-timetable systems`"
	case errors.Is(err, transit.ErrUnknownStop):
		return "hint: find stop IDs with `urbanist-timetable stops search <source> <words>`"
	}
	return ""
}

func newRootCommand() *cli.Command {
	return &cli.Command{
		Name:    "urbanist-timetable",
		Usage:   "show public transit departures in a terminal or on a pixel clock",
		Version: version,
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
				Usage:   "json or text",
				Value:   "json",
				Sources: cli.EnvVars("TIMETABLE_LOG_FORMAT"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "debug, info, warn or error",
				Value:   "info",
				Sources: cli.EnvVars("TIMETABLE_LOG_LEVEL"),
			},
		},
		Commands: []*cli.Command{
			departuresCommand(),
			stopsCommand(),
			systemsCommand(),
			configCommand(),
			clockCommand(),
		},
	}
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

// cacheOptions passes --cache-dir to the providers.
func cacheOptions(c *cli.Command) []providers.Option {
	dir := c.String("cache-dir")
	if dir == "off" {
		dir = ""
	}
	return []providers.Option{providers.WithScheduleCache(dir)}
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

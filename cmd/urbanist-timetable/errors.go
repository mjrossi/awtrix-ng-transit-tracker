package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/termout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

// usageError is a mistake in how a command was called. Its hint points at
// that command's help instead of printing all of it.
type usageError struct {
	msg     string
	command string
}

func (e *usageError) Error() string { return e.msg }

func newUsageError(c *cli.Command, format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...), command: c.FullName()}
}

// sourceError is a source that could not be opened: a name that is no block,
// system or provider, or a provider that needs settings the command line
// cannot give.
type sourceError struct {
	name       string
	configPath string
	suggestion string
	err        error
}

func (e *sourceError) Error() string { return e.err.Error() }
func (e *sourceError) Unwrap() error { return e.err }

// unknownStopsError is returned when no stop a command named exists, which
// usually means stop names were given where IDs belong.
type unknownStopsError struct {
	source string
	stops  []string
	err    error
}

func (e *unknownStopsError) Error() string { return e.err.Error() }
func (e *unknownStopsError) Unwrap() error { return e.err }

// noConfigError is a configuration file a command needs but cannot find.
type noConfigError struct {
	path string
	err  error
}

func (e *noConfigError) Error() string {
	return "no configuration file at " + e.path
}
func (e *noConfigError) Unwrap() error { return e.err }

// printError prints err, one line per joined error, and a hint. Errors can
// quote server responses, so control characters are removed from each line.
func printError(w io.Writer, err error) {
	lines := errorLines(err)
	switch len(lines) {
	case 0:
	case 1:
		fmt.Fprintf(w, "error: %s\n", lines[0])
	default:
		fmt.Fprintln(w, "error:")
		for _, l := range lines {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if h := hint(err); h != "" {
		fmt.Fprintln(w, "hint: "+h)
	}
}

// oneLine is err as one line without control characters.
func oneLine(err error) string {
	return strings.Join(errorLines(err), "; ")
}

// errorLines splits err into its non-empty lines without control
// characters.
func errorLines(err error) []string {
	var lines []string
	for l := range strings.SplitSeq(err.Error(), "\n") {
		if l = strings.TrimSpace(termout.Clean(l)); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// hint suggests what fixes err, or returns "". The library's errors do not
// name commands, since programs other than this one use it.
func hint(err error) string {
	var (
		usage    *usageError
		source   *sourceError
		stops    *unknownStopsError
		noConfig *noConfigError
		missing  *catalog.MissingOptionsError
	)
	switch {
	case errors.As(err, &usage):
		return fmt.Sprintf("run `%s --help`", usage.command)
	case errors.As(err, &noConfig):
		return "create one with `urbanist-timetable config init`, or see `urbanist-timetable config example`"
	case errors.As(err, &missing):
		return "run `urbanist-timetable systems show " + missing.System + "`"
	case errors.As(err, &source) && errors.Is(source, registry.ErrUnknownProvider):
		h := fmt.Sprintf("%q is not a [providers] block in %s, a system, or a provider; ", source.name, source.configPath)
		if source.suggestion != "" {
			return h + fmt.Sprintf("did you mean %q?", source.suggestion)
		}
		return h + "list systems with `urbanist-timetable systems`"
	case errors.As(err, &source):
		return fmt.Sprintf("%s needs settings: add a [providers.%s] block to %s (see `urbanist-timetable config example`)", source.name, source.name, source.configPath)
	case errors.As(err, &stops):
		return fmt.Sprintf("find stop IDs with `urbanist-timetable stops search %s %s`", stops.source, strings.Join(stops.stops, " "))
	case errors.Is(err, catalog.ErrUnknownSystem):
		return "run `urbanist-timetable systems`"
	case errors.Is(err, transit.ErrUnknownStop):
		return "find stop IDs with `urbanist-timetable stops search <source> <words>`"
	}
	return ""
}

// onUsageError replaces urfave/cli's response to a bad flag, which prints
// the whole help, with an error and a hint.
func onUsageError(_ context.Context, c *cli.Command, err error, _ bool) error {
	msg := flagValueMsg.ReplaceAllString(err.Error(), "for --$1")
	if m := undefinedFlagMsg.FindStringSubmatch(msg); m != nil {
		msg = "unknown flag --" + m[1]
		var names []string
		for _, f := range c.VisibleFlags() {
			names = append(names, f.Names()...)
		}
		if s := closest(m[1], names); s != "" {
			msg += fmt.Sprintf("; did you mean --%s?", s)
		}
	}
	return &usageError{msg: msg, command: c.FullName()}
}

// urfave/cli names flags with one dash in its messages.
var (
	flagValueMsg     = regexp.MustCompile(`for flag -+(\S+)`)
	undefinedFlagMsg = regexp.MustCompile(`^flag provided but not defined: -+(\S+)$`)
)

// unknownSubcommand is the action of a command that only groups others. It
// prints the group's help, or rejects an argument that names no subcommand.
func unknownSubcommand(_ context.Context, c *cli.Command) error {
	if !c.Args().Present() {
		if c.Root() == c {
			return cli.ShowRootCommandHelp(c)
		}
		return cli.ShowSubcommandHelp(c)
	}
	return unknownCommandError(c, c.Args().First())
}

func unknownCommandError(c *cli.Command, name string) error {
	var names []string
	for _, sub := range c.VisibleCommands() {
		names = append(names, sub.Names()...)
	}
	names = slices.DeleteFunc(names, func(n string) bool { return n == "help" || n == "h" })
	return newUsageError(c, "unknown command %q%s", name, didYouMean(name, names))
}

// withUsageHandling gives every command in the tree short usage errors, and
// every group without an action a check for unknown subcommands.
func withUsageHandling(root *cli.Command) *cli.Command {
	_ = root.Walk(func(c *cli.Command) error {
		c.OnUsageError = onUsageError
		if c.Action == nil && len(c.Commands) > 0 {
			c.Action = unknownSubcommand
		}
		return nil
	})
	return root
}

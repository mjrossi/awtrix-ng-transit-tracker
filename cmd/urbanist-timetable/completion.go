package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/mjrossi/urbanist-timetable/internal/config"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

// Completion reads only the catalog and the configuration file's names, so
// it works offline and with a configuration that does not validate.

// completeFirst completes a command's first argument from names, and a
// word starting with - from its flags.
func completeFirst(names func(c *cli.Command) []string) cli.ShellCompleteFunc {
	return func(ctx context.Context, c *cli.Command) {
		args := c.Args().Slice()
		switch {
		case len(args) > 0 && strings.HasPrefix(args[len(args)-1], "-"):
			cli.DefaultCompleteWithFlags(ctx, c)
		case len(args) == 0:
			for _, n := range names(c) {
				fmt.Fprintln(c.Root().Writer, n)
			}
		}
	}
}

// completeSources lists what can be queried, with boards first when
// withBoards is set.
func completeSources(withBoards bool) func(c *cli.Command) []string {
	return func(c *cli.Command) []string {
		return dedupe(sourceNames(c.String("config"), withBoards))
	}
}

func completeSystems(*cli.Command) []string {
	all, err := catalog.All()
	if err != nil {
		return nil
	}
	ids := make([]string, len(all))
	for i, s := range all {
		ids[i] = s.ID
	}
	return ids
}

// completeBoardFlag completes the value of --board, and otherwise flags.
func completeBoardFlag(ctx context.Context, c *cli.Command) {
	args := c.Args().Slice()
	if len(args) > 0 && args[len(args)-1] == "--board" {
		boards, _ := config.Names(c.String("config"))
		for _, b := range boards {
			fmt.Fprintln(c.Root().Writer, b)
		}
		return
	}
	cli.DefaultCompleteWithFlags(ctx, c)
}

// showCompletionCommand lists `completion`, which urfave/cli hides.
func showCompletionCommand(c *cli.Command) { c.Hidden = false }

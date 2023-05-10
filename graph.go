package actiongraph

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func addGraphCommand(prog *cobra.Command, opt *options) {
	cmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "graph [-f compile.json] [--why PKG]",
		Short:   "Graphviz visaualisation of the build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			why, err := cmd.Flags().GetString("why")
			if err != nil {
				return err
			}

			return graph(opt, why)
		},
	}
	cmd.Flags().String("why", "", "show only paths to the given package")
	prog.AddCommand(&cmd)
}

func graph(opt *options, why string) error {
	actions := opt.actions

	// Find the first build step.
	start := -1
	for _, act := range actions {
		if act.Mode == "build" {
			start = act.ID
			break
		}
	}
	if start == -1 {
		return errors.New("no first build step")
	}

	// guide is a shortcut set of actions with Deps leading to the destination.
	guide := make([]int, len(actions))
	const (
		avoid   = -1
		unknown = 0
		follow  = 1
	)
	for _, act := range actions {
		switch {
		case act.Mode == "nop":
			guide[act.ID] = avoid
		case why != "" && act.Mode == "build" && act.Package == why:
			guide[act.ID] = follow
		case why == "":
			guide[act.ID] = follow
		}
	}

	// Update the guide to tell us which nodes take us to a node we're
	// interested in.
	// TODO: Should skip if the user didn't filter.
	stack := [][]int{{start}}
	for len(stack) > 0 {
		// Pop the stack.
		depth := len(stack) - 1
		n := stack[depth][0]

		switch guide[n] {
		case avoid:
			// Nothing.
		case unknown:
			// Step into the children.
			if deps := actions[n].Deps; len(deps) > 0 {
				stack = append(stack, deps)
				continue
			}
		case follow:
			// Mark the path to this point as followable.
			for i := range stack {
				guide[stack[i][0]] = follow
			}
		}

		// Trim the stack.
		for d := len(stack) - 1; d >= 0; d-- {
			s := stack[d]
			m := s[0]
			if guide[m] != follow {
				guide[m] = avoid
			}

			if len(s) == 1 {
				stack = stack[:d]
				continue
			}
			stack[d] = s[1:]
			break
		}
	}

	fmt.Fprintln(opt.stdout, "digraph {")
	for i, g := range guide {
		if g != follow {
			continue
		}
		act := actions[i]
		fmt.Fprintf(opt.stdout, "%d [label=<%s>; shape=box];\n", i, "<FONT POINT-SIZE=\"12\">"+filepath.Dir(act.Package)+"</FONT><BR/><FONT POINT-SIZE=\"22\">"+filepath.Base(act.Package)+"</FONT><BR/>"+act.Mode+" "+act.TimeDone.Sub(act.TimeStart).String())

		for _, dep := range act.Deps {
			if guide[dep] != follow {
				continue
			}
			fmt.Printf("\t%d -> %d;\n", i, dep)
		}
	}
	fmt.Fprintln(opt.stdout, "}")

	return nil
}

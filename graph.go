package actiongraph

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

func addGraphCommand(prog *cobra.Command) {
	cmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "graph [-f compile.json] [--why PKG]",
		Short:   "Graphviz visaualisation of the build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			opt, err := loadOptions(cmd)
			if err != nil {
				return err
			}

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

	// show is a shortcut set of actions with Deps leading to the destination.
	show := make([]int, len(actions))
	shown := 0

	// Ignore "nop" nodes.
	for _, act := range actions {
		if act.Mode == "nop" {
			// TODO: What is the Mode:"nop" action? It typically has many Deps
			// that make rendering the graph complicated.
			show[act.ID] = avoid
		}
	}

	if why != "" {
		// Look for our destination node.
		for i, act := range actions {
			if act.Mode == "build" && act.Package == why {
				shown++
				show[i] = follow
				break
			}
		}
		if shown == 0 {
			return fmt.Errorf("could not find package %q", why)
		}
	}

	if shown == 0 {
		// If there are no specific nodes we want, show them all.
		for i, g := range show {
			if g != avoid {
				show[i] = follow
			}
		}
	} else if shown > 0 {
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

		// Show all nodes between the start and the other nodes we want to show.
		pathfind(start, show, func(n int) []int { return actions[n].Deps })
	}

	fmt.Fprintln(opt.stdout, "digraph {")
	for i, g := range show {
		if g != follow {
			continue
		}
		act := actions[i]
		fmt.Fprintf(opt.stdout, "%d [label=<%s>; shape=box];\n", i, "<FONT POINT-SIZE=\"12\">"+filepath.Dir(act.Package)+"</FONT><BR/><FONT POINT-SIZE=\"22\">"+filepath.Base(act.Package)+"</FONT><BR/>"+act.Mode+" "+act.TimeDone.Sub(act.TimeStart).String())

		for _, dep := range act.Deps {
			if show[dep] != follow {
				continue
			}
			fmt.Printf("\t%d -> %d;\n", i, dep)
		}
	}
	fmt.Fprintln(opt.stdout, "}")

	return nil
}

const (
	avoid   = -1
	unknown = 0
	follow  = 1
)

func pathfind(start int, guide []int, edges func(int) []int) {
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
			if deps := edges(n); len(deps) > 0 {
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
}

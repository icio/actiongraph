// Program actiongraph produces Graphviz visualisation of the Go build actiongraph.
// Usage:
//
//	go build -debug-actiongraph=compile.json ./my-program
//	actiongraph tree < compile.json
//	actiongraph dot < compile.json > graph.dot
//	dot -Tsvg graph.dot > graph.svg
//
//	actiongraph -o tree --cover 90% < compile.json
//	actiongraph -o dot --to github.com/unravelin/core/lib/k8s < compile.json
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"

	"github.com/spf13/cobra"
)

func main() {
	prog := cobra.Command{
		Use: "actiongraph",
	}

	// Parse flag -f into the actions, for all programs.
	var actions []action
	prog.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		fn, err := cmd.Flags().GetString("file")
		if err != nil {
			return err
		}

		f, err := openFile(fn)
		if err != nil {
			return err
		}
		defer f.Close()

		if err := json.NewDecoder(f).Decode(&actions); err != nil {
			return fmt.Errorf("decoding JSON: %w", err)
		}

		for i := range actions {
			actions[i].Duration = actions[i].TimeDone.Sub(actions[i].TimeStart)
		}
		return nil
	}
	prog.PersistentFlags().StringP("file", "f", "-", "JSON file to read (use - for stdin)")

	addTopCommand(&prog, &actions)
	addTreeCommand(&prog, &actions)
	addDotCommand(&prog, &actions)

	prog.AddGroup(&cobra.Group{
		ID:    "actiongraph",
		Title: "Actiongraph",
	})
	if err := prog.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "actiongraph: %s\n", err)
		os.Exit(1)
	}
}

func openFile(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

type action struct {
	Duration time.Duration

	ID        int       `json:"ID"`
	Mode      string    `json:"Mode"`
	Package   string    `json:"Package"`
	Deps      []int     `json:"Deps,omitempty"`
	Objdir    string    `json:"Objdir,omitempty"`
	Target    string    `json:"Target,omitempty"`
	Priority  int       `json:"Priority,omitempty"`
	Built     string    `json:"Built,omitempty"`
	BuildID   string    `json:"BuildID,omitempty"`
	TimeReady time.Time `json:"TimeReady"`
	TimeStart time.Time `json:"TimeStart"`
	TimeDone  time.Time `json:"TimeDone"`
	Cmd       any       `json:"Cmd"`
	ActionID  string    `json:"ActionID,omitempty"`
	CmdReal   int       `json:"CmdReal,omitempty"`
	CmdUser   int64     `json:"CmdUser,omitempty"`
	CmdSys    int       `json:"CmdSys,omitempty"`
	NeedBuild bool      `json:"NeedBuild,omitempty"`
}

func addTopCommand(cmd *cobra.Command, actions *[]action) {
	topCmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "top [-f compile.json] [-n limit]",
		Short:   "List slowest build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			return top(*actions)
		},
	}
	topCmd.Flags().IntP("limit", "n", 10, "number of slowest build steps to show")
	cmd.AddCommand(&topCmd)
}

func top(actions []action) error {
	var tot time.Duration
	for i := range actions {
		tot += actions[i].Duration
	}

	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Duration >= actions[j].Duration
	})

	fmt.Println("Time Sec  Cum%\tMode\tPackage")
	var cum time.Duration
	for _, node := range actions {
		cum += node.Duration
		fmt.Printf("% 8.3f % 4d%%\t%s\t%s\n", node.Duration.Seconds(), int(100*float64(cum)/float64(tot)), node.Mode, node.Package)
	}
	return nil
}

func addTreeCommand(prog *cobra.Command, actions *[]action) {
	cmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "tree [-m] [-f compile.json] [package...]",
		Short:   "Total build times by directory",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			level, err := flags.GetInt("level")
			if err != nil {
				return nil
			}

			return tree(*actions, level, args)
		},
	}

	flags := cmd.Flags()
	flags.IntP("level", "L", -1, "descend only level directories deep (-ve for unlimited)")

	prog.AddCommand(&cmd)
}

func tree(actions []action, level int, focus []string) error {
	root := buildTree(actions)

	if len(focus) != 0 {
		filterActs := make([]action, len(focus))
		for i, pkg := range focus {
			filterActs[i] = action{
				ID:      0,       // buildTree and pruneTree use -1 for intermediary nodes.
				Mode:    "build", // buildTree ignores non-build actions.
				Package: strings.TrimRight(pkg, "/."),
			}
		}
		pruneTree(root, buildTree(filterActs))
	}

	dirs := append(make([][]*pkgtree, 0, 10), []*pkgtree{root})
	for len(dirs) > 0 {
		// Step up from empty paths.
		last := len(dirs) - 1
		if len(dirs[last]) == 0 {
			dirs = dirs[:last]
			continue
		}

		// Take the next node.
		n := dirs[last][0]
		dirs[last] = dirs[last][1:]
		if level >= 0 && n.depth > level {
			continue
		}

		// Display the node.
		var ds string
		if n.id == -1 {
			ds = "        "
		} else {
			ds = fmtD(actions[n.id].Duration)
		}
		fmt.Printf("%s %s %s%s\n", fmtD(n.d), ds, strings.Repeat("  ", last), n.path)

		// Step into the children.
		if len(n.dir) > 0 {
			kids := maps.Values(n.dir)
			slices.SortFunc(kids, func(a, b *pkgtree) bool { return a.d > b.d })
			dirs = append(dirs, kids)
			continue
		}
	}
	return nil
}

type pkgtree struct {
	path  string
	depth int
	d     time.Duration
	id    int

	dir map[string]*pkgtree
}

func buildTree(actions []action) *pkgtree {
	root := pkgtree{
		path: "/ (total)",
		id:   -1,
	}

	// Loop over each built package path.
	for _, act := range actions {
		if act.Mode != "build" {
			continue
		}

		// Assume all packages without a "." are part of the standard library.
		// TODO: Go modules don't need to start with a domain, so this is wrong.
		pkg := act.Package
		if !strings.Contains(pkg, ".") {
			pkg = "std/" + pkg
		}

		// Create the tree of nodes for this one package.
		actNode := &root
		actNode.d += act.Duration
		p := 0
		depth := 0
		for more := true; more; {
			depth++

			// Read the next deepest path from pkg.
			pn := strings.Index(pkg[p+1:], "/")
			if pn == -1 {
				p = len(pkg)
				more = false
			} else {
				p += pn + 1
			}
			path := pkg[:p]

			// Ensure a node for this path exists.
			if actNode.dir == nil {
				actNode.dir = make(map[string]*pkgtree, 1)
			}
			p := actNode.dir[path]
			if p == nil {
				p = &pkgtree{
					id:    -1,
					path:  path,
					depth: depth,
				}
				actNode.dir[path] = p
			}

			// Descend into the node for this path.
			actNode = p
			actNode.d += act.Duration
		}

		actNode.id = act.ID
	}
	return &root
}

// pruneTree removes dir grandchildren from root that are not in keep and resets
// the depth according to the closest grandchild in keep.
func pruneTree(root, keep *pkgtree) {
	type job struct{ r, k *pkgtree }
	work := make([]job, 0, 10)
	work = append(work, job{root, keep})

	// TODO: actiongraph tree pkg1 pkg1/pkg2/pkg3 -L 0 should show
	// pkg1/pkg2/pkg3 but we don't currently traverse past pkg1.
	for len(work) > 0 {
		j := work[len(work)-1]
		work = work[:len(work)-1]

		// TODO: Can we consolidate these two cases?
		if j.k.id == -1 {
			// Branch nodes in keep have ID -1. We want to kepe the common
			// children of root and keep.
			for path, rChild := range j.r.dir {
				rChild.depth = 0
				kChild := j.k.dir[path]
				if kChild == nil {
					delete(j.r.dir, path)
				} else {
					work = append(work, job{rChild, kChild})
				}
			}
		} else {
			// This path was explicitly added to keep. We want to reset the
			// depth of each child from this point.
			for path, rChild := range j.r.dir {
				rChild.depth -= j.k.depth
				kChild := j.k.dir[path]
				if kChild == nil {
					kChild = &pkgtree{
						id:    0,
						path:  path,
						depth: j.k.depth, // Keep this depth.
					}
				}
				work = append(work, job{rChild, kChild})
			}
		}
	}
}

func fmtD(d time.Duration) string {
	return fmt.Sprintf("% 8.3f", d.Seconds())
}

func addDotCommand(prog *cobra.Command, actions *[]action) {
	cmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "dot [-f compile.json] [--why PKG]",
		Short:   "Graphviz visaualisation of the build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			why, err := cmd.Flags().GetString("why")
			if err != nil {
				return err
			}

			return dot(*actions, why)
		},
	}
	cmd.Flags().String("why", "", "show only paths to the given package")
	prog.AddCommand(&cmd)
}

func dot(actions []action, why string) error {
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

	fmt.Println("digraph {")
	for i, g := range guide {
		if g != follow {
			continue
		}
		act := actions[i]

		// secs := strings.Split(act.Package, "/")
		// secs = secs[:len(secs)-1]
		// for _, sec := range secs {
		// 	fmt.Printf("subgraph %q { graph[label=%q] ", "cluster_"+sec, sec)
		// }
		fmt.Printf("%d [label=<%s>; shape=box];\n", i, "<FONT POINT-SIZE=\"12\">"+filepath.Dir(act.Package)+"</FONT><BR/><FONT POINT-SIZE=\"22\">"+filepath.Base(act.Package)+"</FONT><BR/>"+act.Mode+" "+act.TimeDone.Sub(act.TimeStart).String())
		// for range secs {
		// 	fmt.Print(" }")
		// }
		fmt.Println()

		for _, dep := range act.Deps {
			if guide[dep] != follow {
				continue
			}
			fmt.Printf("\t%d -> %d;\n", i, dep)
		}
	}
	fmt.Println("}")

	return nil
}

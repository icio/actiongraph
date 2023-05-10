// Program actiongraph produces Graphviz visualisation of the Go build actiongraph.
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
	txttpl "text/template"
	"time"

	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"

	"github.com/spf13/cobra"
)

func main() {
	prog := cobra.Command{
		Use:           "actiongraph",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Parse the global, shared options and data.
	opt := options{
		out: os.Stdout,
		funcs: txttpl.FuncMap{
			"base": filepath.Base,
			"dir":  filepath.Dir,
			"seconds": func(d time.Duration) string {
				return fmt.Sprintf("%.3fs", d.Seconds())
			},
			"percent": func(v float64) string {
				return fmt.Sprintf("%.2f%%", v)
			},
			"right": func(n int, s string) string {
				if len(s) > n {
					return s
				}
				return strings.Repeat(" ", n-len(s)) + s
			},
		},
	}
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

		if err := json.NewDecoder(f).Decode(&opt.actions); err != nil {
			return fmt.Errorf("decoding input: %w", err)
		}

		for i := range opt.actions {
			// TODO: Flag to look at CmdReal/CmdUser instead? We can use the Cmd
			// field being non-null to differentiate between cached and
			// non-cached steps, too.
			d := opt.actions[i].TimeDone.Sub(opt.actions[i].TimeStart)
			opt.actions[i].Duration = d
			opt.total += d
		}
		for i := range opt.actions {
			opt.actions[i].Percent = 100 * float64(opt.actions[i].Duration) / float64(opt.total)
		}
		return nil
	}
	prog.PersistentFlags().StringP("file", "f", "-", "JSON file to read (use - for stdin)")

	addTopCommand(&prog, &opt)
	addTreeCommand(&prog, &opt)
	addDotCommand(&prog, &opt)

	prog.AddGroup(&cobra.Group{
		ID:    "actiongraph",
		Title: "Actiongraph",
	})
	if err := prog.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "actiongraph: %s\n", err)
		os.Exit(1)
	}
}

type options struct {
	out     io.Writer
	funcs   txttpl.FuncMap
	actions []action
	total   time.Duration
}

func openFile(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

type action struct {
	ID        int
	Mode      string
	Package   string
	Deps      []int
	Objdir    string
	Target    string
	Priority  int
	Built     string
	BuildID   string
	TimeReady time.Time
	TimeStart time.Time
	TimeDone  time.Time
	Cmd       any
	ActionID  string
	CmdReal   int
	CmdUser   int64
	CmdSys    int
	NeedBuild bool

	Duration time.Duration
	Percent  float64
}

func addTopCommand(cmd *cobra.Command, opt *options) {
	topCmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "top [-f compile.json] [-n limit]",
		Short:   "List slowest build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			limit, err := flags.GetInt("limit")
			if err != nil {
				return err
			}

			tplStr, err := flags.GetString("tpl")
			if err != nil {
				return err
			}
			tpl, err := txttpl.New("top").Funcs(opt.funcs).Parse(tplStr)
			if err != nil {
				return fmt.Errorf("parsing tpl: %w", err)
			}

			return top(opt, limit, tpl)
		},
	}
	flags := topCmd.Flags()
	flags.IntP("limit", "n", 20, "number of slowest build steps to show")
	flags.String("tpl", `{{ .Duration | seconds | right 8 }}{{ .CumulativePercent | percent | right 8 }}  {{.Mode}}	{{.Package}}`, "template for output")
	cmd.AddCommand(&topCmd)
}

func top(opt *options, limit int, tpl *txttpl.Template) error {
	actions := opt.actions

	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Duration >= actions[j].Duration
	})

	var cum time.Duration
	for i, node := range actions {
		if limit > 0 && i >= limit {
			break
		}

		cum += node.Duration
		err := tpl.Execute(opt.out, topAction{
			action:            node,
			Percent:           100 * float64(node.Duration) / float64(opt.total),
			CumulativePercent: 100 * float64(cum) / float64(opt.total),
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(opt.out)
	}
	return nil
}

type topAction struct {
	action
	Percent           float64
	CumulativePercent float64
}

func addTreeCommand(prog *cobra.Command, opt *options) {
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

			tplStr, err := flags.GetString("tpl")
			if err != nil {
				return err
			}
			tpl, err := txttpl.New("top").Funcs(opt.funcs).Parse(tplStr)
			if err != nil {
				return fmt.Errorf("parsing tpl: %w", err)
			}

			return tree(opt, level, args, tpl)
		},
	}

	flags := cmd.Flags()
	flags.IntP("level", "L", -1, "descend only level directories deep (-ve for unlimited)")
	flags.String("tpl", `{{ .CumulativeDuration | seconds | right 8 }}{{ if eq .ID -1 }}          {{ else }}{{ .Duration | seconds | right 8 }}{{ end }} {{.Indent}}{{.Package}}`, "template for output")

	prog.AddCommand(&cmd)
}

type treeAction struct {
	ID                 int
	Package            string
	Indent             string
	Depth              int
	CumulativeDuration time.Duration
	CumulativePercent  float64
	action
}

func tree(opt *options, level int, focus []string, tpl *txttpl.Template) error {
	actions := opt.actions
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
		node := treeAction{
			ID:                 n.id,
			Package:            n.path,
			Depth:              n.depth,
			Indent:             strings.Repeat("  ", last),
			CumulativePercent:  100 * float64(n.d) / float64(opt.total),
			CumulativeDuration: n.d,
		}
		if n.id > 0 {
			node.action = actions[n.id]
		}
		err := tpl.Execute(opt.out, node)
		if err != nil {
			return err
		}
		fmt.Fprintln(opt.out)

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
		path: "(root)",
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
		if isStdlib(pkg) {
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

func isStdlib(pkg string) bool {
	root, _, _ := strings.Cut(pkg, "/")
	return !strings.Contains(root, ".")
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

func addDotCommand(prog *cobra.Command, opt *options) {
	cmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "dot [-f compile.json] [--why PKG]",
		Short:   "Graphviz visaualisation of the build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			why, err := cmd.Flags().GetString("why")
			if err != nil {
				return err
			}

			return dot(opt, why)
		},
	}
	cmd.Flags().String("why", "", "show only paths to the given package")
	prog.AddCommand(&cmd)
}

func dot(opt *options, why string) error {
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

	fmt.Fprintln(opt.out, "digraph {")
	for i, g := range guide {
		if g != follow {
			continue
		}
		act := actions[i]
		fmt.Fprintf(opt.out, "%d [label=<%s>; shape=box];\n", i, "<FONT POINT-SIZE=\"12\">"+filepath.Dir(act.Package)+"</FONT><BR/><FONT POINT-SIZE=\"22\">"+filepath.Base(act.Package)+"</FONT><BR/>"+act.Mode+" "+act.TimeDone.Sub(act.TimeStart).String())

		for _, dep := range act.Deps {
			if guide[dep] != follow {
				continue
			}
			fmt.Printf("\t%d -> %d;\n", i, dep)
		}
	}
	fmt.Fprintln(opt.out, "}")

	return nil
}

package actiongraph

import (
	"fmt"
	"maps"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/exp/slices"
)

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
			tpl, err := template.New("top").Funcs(opt.funcs).Parse(tplStr)
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

func tree(opt *options, level int, focus []string, tpl *template.Template) error {
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
		err := tpl.Execute(opt.stdout, node)
		if err != nil {
			return err
		}
		fmt.Fprintln(opt.stdout)

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

type treeAction struct {
	ID                 int
	Package            string
	Indent             string
	Depth              int
	CumulativeDuration time.Duration
	CumulativePercent  float64
	action
}

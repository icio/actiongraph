package actiongraph

import (
	"fmt"
	"sort"
	"text/template"
	"time"

	"github.com/spf13/cobra"
)

func addTopCommand(cmd *cobra.Command) {
	topCmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "top [-f compile.json] [-n limit]",
		Short:   "List slowest build steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			opt, err := loadOptions(cmd)
			if err != nil {
				return err
			}

			flags := cmd.Flags()
			limit, err := flags.GetInt("limit")
			if err != nil {
				return err
			}

			tplStr, err := flags.GetString("tpl")
			if err != nil {
				return err
			}
			tpl, err := template.New("top").Funcs(opt.funcs).Parse(tplStr)
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

func top(opt *options, limit int, tpl *template.Template) error {
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
		err := tpl.Execute(opt.stdout, topAction{
			action:            node,
			CumulativePercent: 100 * float64(cum) / float64(opt.total),
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(opt.stdout)
	}
	return nil
}

type topAction struct {
	action
	CumulativePercent float64
}

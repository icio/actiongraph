package main

import (
	"fmt"
	"sort"
	"text/template"
	"time"

	"golang.org/x/exp/maps"

	"github.com/spf13/cobra"
)

func addTypesCommand(cmd *cobra.Command) {
	topCmd := cobra.Command{
		GroupID: "actiongraph",
		Use:     "types [-f compile.json] [-n limit]",
		Short:   "List slowest action types",
		RunE: func(cmd *cobra.Command, args []string) error {
			opt, err := loadOptions(cmd)
			if err != nil {
				return err
			}

			flags := cmd.Flags()

			tplStr, err := flags.GetString("tpl")
			if err != nil {
				return err
			}
			tpl, err := template.New("top").Funcs(opt.funcs).Parse(tplStr)
			if err != nil {
				return fmt.Errorf("parsing tpl: %w", err)
			}

			return typesTop(opt, tpl)
		},
	}
	flags := topCmd.Flags()
	flags.String("tpl", `{{ .Duration | seconds | right 8 }}{{ .Percentage | percent | right 8 }}  {{.Mode}}`, "template for output")
	cmd.AddCommand(&topCmd)
}

func typesTop(opt *options, tpl *template.Template) error {
	actions := opt.actions
	types := map[string]typesAction{}
	var cum time.Duration
	for _, node := range actions {
		cum += node.Duration
		ta, f := types[node.Mode]
		if !f {
			ta = typesAction{Mode: node.Mode}
		}
		ta.Duration += node.Duration
		ta.Percentage = 100 * float64(ta.Duration) / float64(opt.total)
		types[node.Mode] = ta
	}
	actionTypes := maps.Values(types)
	sort.Slice(actionTypes, func(i, j int) bool {
		return actionTypes[i].Duration >= actionTypes[j].Duration
	})

	for _, node := range actionTypes {
		err := tpl.Execute(opt.stdout, node)
		if err != nil {
			return err
		}
		fmt.Fprintln(opt.stdout)
	}
	return nil
}

type typesAction struct {
	Mode       string
	Duration   time.Duration
	Percentage float64
}

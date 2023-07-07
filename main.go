package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	txttpl "text/template"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	err := run(os.Args[1:]...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "actiongraph: %s\n", err)
		os.Exit(1)
	}
}

func run(args ...string) error {
	prog := &cobra.Command{
		Use:           "actiongraph",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	prog.PersistentFlags().StringP("file", "f", "-", "JSON file to read (use - for stdin)")
	prog.MarkFlagRequired("file")
	prog.RegisterFlagCompletionFunc("file", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return []string{"json"}, cobra.ShellCompDirectiveFilterFileExt
	})

	addTopCommand(prog)
	addTreeCommand(prog)
	addTypesCommand(prog)
	addGraphCommand(prog)

	prog.AddGroup(&cobra.Group{
		ID:    "actiongraph",
		Title: "Actiongraph:",
	})

	prog.SetArgs(args)
	return prog.Execute()
}

type options struct {
	stdin   io.Reader
	stdout  io.Writer
	args    []string
	funcs   txttpl.FuncMap
	actions []action
	total   time.Duration
}

func loadOptions(cmd *cobra.Command) (*options, error) {
	opt := options{
		stdin:  cmd.InOrStdin(),
		stdout: cmd.OutOrStdout(),
		args:   cmd.Flags().Args(),

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

	// Open the actiongraph JSON file.
	fn, err := cmd.Flags().GetString("file")
	if err != nil {
		return nil, err
	}
	f, err := openFile(fn)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Decode the actions.
	if err := json.NewDecoder(f).Decode(&opt.actions); err != nil {
		return nil, fmt.Errorf("decoding input: %w", err)
	}

	// A few top-level calculations.
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
	return &opt, nil
}

func openFile(path string) (*os.File, error) {
	switch path {
	case "", "-", "/dev/stdin", "/dev/fd/0":
		return os.Stdin, nil
	default:
		return os.Open(path)
	}
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

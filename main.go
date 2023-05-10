package actiongraph

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

func Main() {
	err := Run(DefaultArgs())
	if err != nil {
		fmt.Fprintf(os.Stderr, "actiongraph: %s\n", err)
		os.Exit(1)
	}
}

func DefaultArgs() Args {
	return Args{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		args:   os.Args[1:],
	}
}

func Run(args Args) error {
	prog := cobra.Command{
		Use:           "actiongraph",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Parse the global, shared options and data.
	opt := options{
		Args: args,
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
		// Open the actiongraph JSON file.
		fn, err := cmd.Flags().GetString("file")
		if err != nil {
			return err
		}
		f, err := openFile(opt.stdin, fn)
		if err != nil {
			return err
		}
		defer f.Close()

		// Decode the actions.
		if err := json.NewDecoder(f).Decode(&opt.actions); err != nil {
			return fmt.Errorf("decoding input: %w", err)
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
		return nil
	}
	prog.PersistentFlags().StringP("file", "f", "-", "JSON file to read (use - for stdin)")

	addTopCommand(&prog, &opt)
	addTreeCommand(&prog, &opt)
	addGraphCommand(&prog, &opt)

	prog.AddGroup(&cobra.Group{
		ID:    "actiongraph",
		Title: "Actiongraph",
	})

	prog.SetArgs(args.args)
	return prog.Execute()
}

type Args struct {
	stdin  io.Reader
	stdout io.Writer
	args   []string
}

type options struct {
	Args
	funcs   txttpl.FuncMap
	actions []action
	total   time.Duration
}

func openFile(stdin io.Reader, path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(stdin), nil
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

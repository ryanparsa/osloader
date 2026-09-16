package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryanparsa/osloader/internal/provider"
)

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the installers available in a channel",
		RunE: func(cmd *cobra.Command, _ []string) error {
			applyProviderFlags()
			p, err := selectedProvider()
			if err != nil {
				return err
			}

			mode, err := provider.ParseSortMode(opts.sortBy)
			if err != nil {
				return err
			}

			channel := channelFor(cmd, p)
			sel, err := selection(p, channel)
			if err != nil {
				return err
			}

			releases, err := p.List(cmd.Context(), channel, sel)
			if err != nil {
				return err
			}
			provider.Sort(releases, mode, opts.reverse)

			if opts.jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(releases)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PRODUCT\tVERSION\tBUILD\tPOSTED\tSIZE\tTITLE")
			for _, r := range releases {
				posted := "—"
				if !r.Posted.IsZero() {
					posted = r.Posted.Local().Format("2006-01-02")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					r.ID, orDash(r.Version), orDash(r.Build), posted, humanBytes(r.Size), orDash(r.Title))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "emit JSON instead of a table")
	cmd.Flags().StringVar(&opts.sortBy, "sort", "date", "sort by date, version, size or name")
	cmd.Flags().BoolVar(&opts.reverse, "reverse", false, "reverse the sort order")
	return cmd
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

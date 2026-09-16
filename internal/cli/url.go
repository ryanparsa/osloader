package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func urlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Print the direct download URL for a release",
		RunE: func(cmd *cobra.Command, _ []string) error {
			applyProviderFlags()
			p, err := selectedProvider()
			if err != nil {
				return err
			}

			channel := channelFor(cmd, p)
			sel, err := selection(p, channel)
			if err != nil {
				return err
			}
			release, err := pickRelease(cmd.Context(), p, channel, sel)
			if err != nil {
				return err
			}
			artifact, err := resolveArtifact(cmd.Context(), p, release, sel)
			if err != nil {
				return err
			}

			fmt.Println(artifact.URL)
			return nil
		},
	}
	addSelectorFlags(cmd)
	return cmd
}

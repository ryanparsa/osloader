package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryanparsa/osloader/internal/download"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/macos"
	"github.com/ryanparsa/osloader/internal/tui"
	"github.com/ryanparsa/osloader/internal/ua"
	"github.com/ryanparsa/osloader/internal/verify"
)

func downloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download an installer and verify it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			applyProviderFlags()
			p, err := selectedProvider()
			if err != nil {
				return err
			}
			limit, err := parseRate(opts.limitRate)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			fmt.Fprintln(os.Stderr, "fetching catalog…")
			channel := channelFor(cmd, p)
			sel, err := selection(p, channel)
			if err != nil {
				return err
			}
			release, err := pickRelease(ctx, p, channel, sel)
			if err != nil {
				return err
			}
			artifact, err := resolveArtifact(ctx, p, release, sel)
			if err != nil {
				return err
			}

			if err := os.MkdirAll(opts.out, 0o755); err != nil {
				return err
			}
			target := artifact.Target(opts.out)
			target.URLs = append(target.URLs, opts.mirrors...)

			settings := download.Settings{
				Connections: opts.connections,
				LimitRate:   limit,
				SkipVerify:  opts.noVerify,
				UserAgent:   ua.Resolve(opts.userAgent),
				Logger:      verboseLogger(),
			}

			// On a terminal this is the same screen the picker uses; piped
			// output stays plain so logs and scripts keep working.
			if isTerminal() {
				return tui.RunDownload(ctx, tui.Config{
					OutDir:      opts.out,
					Connections: opts.connections,
					LimitRate:   limit,
					UserAgent:   opts.userAgent,
					Verbose:     opts.verbose,
					SkipVerify:  opts.noVerify,
				}, tui.Direct{
					OSKey:     opts.osName,
					Channel:   channel,
					Selection: sel,
					Release:   release,
					Artifact:  artifact,
					Mirrors:   opts.mirrors,
				})
			}

			fmt.Printf("%s %s (%s) - %s\n", p.Name(), orDash(release.Version), orDash(release.Build), humanBytes(artifact.Size))
			fmt.Printf("from %s\n", artifact.URL)
			for _, extra := range target.URLs[1:] {
				fmt.Printf("     %s (mirror)\n", extra)
			}
			fmt.Printf("to   %s\n\n", target.Dest)

			outcome, err := download.Fetch(ctx, target, settings, progressPrinter())
			if err != nil {
				return err
			}

			if outcome.AlreadyPresent {
				fmt.Printf("\nalready downloaded and verified: %s\n", outcome.Path)
			} else {
				fmt.Printf("\nsaved %s\n", outcome.Path)
			}
			printReport(outcome.Report, artifact.Digest)
			return nil
		},
	}
	addSelectorFlags(cmd)
	cmd.Flags().BoolVar(&opts.noVerify, "no-verify", false, "skip verification (not recommended)")
	cmd.Flags().StringArrayVar(&opts.mirrors, "mirror", nil, "additional source for the same file; repeatable")
	cmd.Flags().StringVar(&opts.packageName, "package", "", "fetch a named companion package instead of the installer")
	_ = cmd.Flags().MarkHidden("package")
	return cmd
}

// addSelectorFlags adds the three ways of naming a release.
func addSelectorFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&opts.build, "build", "", "select by build, e.g. 26A428")
	cmd.Flags().StringVar(&opts.version, "version", "", "select by version, e.g. 27.0 or 27")
	cmd.Flags().StringVar(&opts.product, "product", "", "select by catalog product ID")
}

// resolveArtifact honours the hidden --package flag, which exists so the whole
// pipeline can be exercised against a tiny file instead of a 17 GiB one.
func resolveArtifact(ctx context.Context, p provider.Provider, r provider.Release, sel provider.Selection) (provider.Artifact, error) {
	if opts.packageName != "" {
		if mp, ok := p.(*macos.Provider); ok {
			return mp.ResolveNamed(ctx, r, opts.packageName)
		}
	}
	return p.Resolve(ctx, r, sel)
}

// progressPrinter repaints one line on a terminal and prints a line every few
// seconds when output is redirected, so logs stay readable.
func progressPrinter() func(download.Snapshot) {
	// In verbose mode the log is streaming to stderr, so a repainting line
	// would be overwritten constantly: print discrete lines instead.
	tty := isTerminal() && !opts.verbose
	var lastPrint time.Time

	return func(s download.Snapshot) {
		if !tty {
			interval := 5 * time.Second
			if opts.verbose {
				interval = 2 * time.Second
			}
			if time.Since(lastPrint) < interval {
				return
			}
			lastPrint = time.Now()
			fmt.Printf("%.1f%% %s/%s %s\n", s.Fraction()*100,
				humanBytes(s.Done), humanBytes(s.Total), humanRate(s.Speed))
			return
		}
		fmt.Printf("\r\033[K%5.1f%%  %s / %s  %s  ETA %s",
			s.Fraction()*100, humanBytes(s.Done), humanBytes(s.Total),
			humanRate(s.Speed), humanDuration(s.ETA))
	}
}

// printReport shows every check, including the ones that only inform, so the
// user can see exactly what "verified" meant here.
func printReport(report *verify.Report, digest string) {
	if report == nil {
		fmt.Println("\nverification skipped (--no-verify)")
		return
	}
	fmt.Println()
	for _, c := range report.Checks {
		marker := "✓"
		switch c.Status {
		case verify.Fail:
			marker = "✗"
		case verify.Warn:
			marker = "!"
		case verify.Info:
			marker = " "
		}
		fmt.Printf("  %s %-10s %s\n", marker, c.Name, c.Detail)
	}
	if digest != "" {
		fmt.Printf("    %-10s %s (vendor value, not a file hash)\n", "digest", digest)
	}
}

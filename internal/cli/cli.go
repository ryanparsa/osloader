// Package cli wires the command line: subcommands for scripting, and the TUI
// when osloader is run with no arguments.
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/provider"
	"github.com/ryanparsa/osloader/internal/provider/debian"
	"github.com/ryanparsa/osloader/internal/provider/macos"
	"github.com/ryanparsa/osloader/internal/tui"
	"github.com/ryanparsa/osloader/internal/ua"

	// Registered for their side effects: each package adds itself to the
	// provider registry.
	_ "github.com/ryanparsa/osloader/internal/provider/debian"
	_ "github.com/ryanparsa/osloader/internal/provider/windows"
)

// options holds the flag values shared by the subcommands. Defaults live here
// in code - there is no config file to read.
type options struct {
	osName      string
	channel     string
	out         string
	connections int
	limitRate   string
	catalogURL  string
	userAgent   string
	noVerify    bool
	packageName string
	jsonOut     bool
	sortBy      string
	reverse     bool
	verbose     bool
	mirrors     []string
	filters     []string

	build   string
	version string
	product string
}

var opts options

// Execute runs the root command and exits non-zero on failure.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rootCmd().ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "\ninterrupted - progress saved, run the same command again to resume")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "osloader",
		Short: "List, download and verify operating-system installers",
		Long: "osloader finds official OS installers, downloads them with parallel\n" +
			"resumable connections, and verifies that what landed on disk really is\n" +
			"the vendor's file.\n\nRun with no arguments for the interactive picker.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd.Context())
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.osName, "os", "macos", "operating system to work with")
	flags.StringVar(&opts.channel, "channel", "public", "release channel")
	flags.StringVar(&opts.out, "out", ".", "directory to download into")
	flags.IntVar(&opts.connections, "connections", 8, "parallel connections (max 16)")
	flags.StringVar(&opts.limitRate, "limit-rate", "", "cap the transfer rate, e.g. 20M")
	flags.StringVar(&opts.catalogURL, "catalog-url", "", "override the vendor catalog URL")
	flags.StringVar(&opts.userAgent, "user-agent", "default", "User-Agent to send: default, browser, or a literal string")
	flags.BoolVarP(&opts.verbose, "verbose", "v", false, "log what is happening: requests, chunks, retries, checks")
	flags.StringArrayVar(&opts.filters, "filter", nil, "narrow the list, e.g. --filter arch=amd64; repeatable")

	root.AddCommand(listCmd(), downloadCmd(), urlCmd())
	return root
}

func runTUI(ctx context.Context) error {
	applyProviderFlags()
	limit, err := parseRate(opts.limitRate)
	if err != nil {
		return err
	}
	return tui.Run(ctx, tui.Config{
		OutDir:      opts.out,
		Connections: opts.connections,
		LimitRate:   limit,
		UserAgent:   ua.Resolve(opts.userAgent),
		Verbose:     opts.verbose,
	})
}

// applyProviderFlags passes the macOS-specific flags to that provider before
// any request goes out.
func applyProviderFlags() {
	if opts.catalogURL != "" {
		macos.SetCatalogURL(opts.catalogURL)
	}
	macos.SetUserAgent(opts.userAgent)
	macos.SetLogger(verboseLogger())
	debian.SetUserAgent(opts.userAgent)
	debian.SetLogger(verboseLogger())
}

// verboseLogger writes the event log to stderr, so stdout stays usable for
// piping list --json or url into another command.
func verboseLogger() *slog.Logger {
	return logging.New(os.Stderr, opts.verbose)
}

// channelFor honours --channel when the user set it, and otherwise uses the
// provider's own default - "public" means nothing to Debian, and "stable"
// means nothing to Apple.
func channelFor(cmd *cobra.Command, p provider.Provider) string {
	if cmd.Flags().Changed("channel") || cmd.Root().PersistentFlags().Changed("channel") {
		return opts.channel
	}
	if channels := p.Channels(); len(channels) > 0 {
		return channels[0].ID
	}
	return opts.channel
}

// selection turns the repeated --filter flags into the answers a provider's
// facets are asking for, and rejects anything that OS does not offer.
func selection(p provider.Provider, channel string) (provider.Selection, error) {
	sel := provider.Selection{}
	for _, raw := range opts.filters {
		key, value, err := provider.ParseFilter(raw)
		if err != nil {
			return nil, err
		}
		sel = sel.With(key, value)
	}
	if err := provider.Validate(p.Facets(channel), sel); err != nil {
		return nil, err
	}
	return sel, nil
}

// selectedProvider resolves --os to a provider and rejects the stubs early,
// with a message that says what is coming rather than just "unknown".
func selectedProvider() (provider.Provider, error) {
	p, err := provider.Get(opts.osName)
	if err != nil {
		return nil, err
	}
	if !p.Available() {
		return nil, fmt.Errorf("%s support is not implemented yet - available today: %s",
			p.Name(), strings.Join(availableNames(), ", "))
	}
	return p, nil
}

// selector is how a user names a release on the command line. Every field that
// is set must match: a fully specified command (as the TUI prints for sharing)
// then either fetches exactly that release or fails loudly, rather than
// quietly matching something else.
type selector struct {
	product string
	version string
	build   string
}

func (s selector) empty() bool {
	return s.product == "" && s.version == "" && s.build == ""
}

func (s selector) matches(r provider.Release) bool {
	switch {
	case s.product != "" && r.ID != s.product:
		return false
	case s.build != "" && r.Build != s.build:
		return false
	case s.version != "" && !provider.MatchVersion(r.Version, s.version):
		return false
	}
	return true
}

func (s selector) String() string {
	parts := make([]string, 0, 3)
	for _, f := range []struct{ name, value string }{
		{"--product", s.product}, {"--version", s.version}, {"--build", s.build},
	} {
		if f.value != "" {
			parts = append(parts, f.name+" "+f.value)
		}
	}
	return strings.Join(parts, " ")
}

// availableNames lists the operating systems that are actually implemented.
func availableNames() []string {
	var names []string
	for _, key := range provider.Keys() {
		if p, err := provider.Get(key); err == nil && p.Available() {
			names = append(names, p.Name())
		}
	}
	return names
}

// pickRelease finds the release named by --build, --version and --product.
// Ties are broken by post date, and the choice is reported so a script's
// "--version 27" is never ambiguous in hindsight.
func pickRelease(ctx context.Context, p provider.Provider, channel string, sel provider.Selection) (provider.Release, error) {
	want := selector{product: opts.product, version: opts.version, build: opts.build}
	if want.empty() {
		return provider.Release{}, errors.New("choose a release with --build, --version or --product")
	}

	releases, err := p.List(ctx, channel, sel)
	if err != nil {
		return provider.Release{}, err
	}

	var matches []provider.Release
	for _, r := range releases {
		if want.matches(r) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return provider.Release{}, fmt.Errorf("no release in the %s channel matched %s", channel, want)
	}

	provider.SortReleases(matches)
	chosen := matches[0]
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "%d releases matched %s; taking the newest: %s (%s), posted %s\n",
			len(matches), want, chosen.Version, chosen.Build, chosen.Posted.Local().Format("2006-01-02"))
	}
	return chosen, nil
}

package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ryanparsa/osloader/internal/logging"
	"github.com/ryanparsa/osloader/internal/verify"
)

// Target is one file to fetch and the means of proving it afterwards.
type Target struct {
	URLs          []string // mirrors of the same file; the first is the primary
	Dest          string   // full destination path
	Size          int64
	AllowedHosts  []string
	MaxPerHost    int  // connections one host may get; 0 means no limit
	PreferPrimary bool // use the first source, keeping the rest as spares
	Verifier      verify.Verifier
}

// URL is the primary source, for messages that name one.
func (t Target) URL() string {
	if len(t.URLs) == 0 {
		return ""
	}
	return t.URLs[0]
}

// Settings are the knobs a user turns.
type Settings struct {
	Connections int
	LimitRate   int64
	SkipVerify  bool
	UserAgent   string
	Logger      *slog.Logger // verbose event log; nil discards
}

// Outcome is what came of a fetch.
type Outcome struct {
	Path           string
	Report         *verify.Report
	AlreadyPresent bool
}

// ErrUnverified means the bytes arrived but could not be shown to be genuine.
// The file is kept, renamed, so it can be inspected — but never under the name
// a trusted installer would have.
var ErrUnverified = errors.New("downloaded file failed verification")

// Fetch downloads a target, verifies it, and only then gives it its real name.
// progress, if non-nil, is called a few times a second while bytes move.
func Fetch(ctx context.Context, t Target, s Settings, progress func(Snapshot)) (*Outcome, error) {
	if s.Logger == nil {
		s.Logger = logging.Discard()
	}
	s.Logger.Info("fetch", "url", t.URL(), "mirrors", len(t.URLs),
		"dest", t.Dest, "size", t.Size, "connections", s.Connections)

	if present, outcome, err := checkExisting(ctx, t, s); present {
		s.Logger.Info("already present", "path", t.Dest)
		return outcome, err
	}

	engine, err := New(Options{
		URLs:          t.URLs,
		Dest:          t.Dest,
		Size:          t.Size,
		Connections:   s.Connections,
		AllowedHosts:  t.AllowedHosts,
		LimitRate:     s.LimitRate,
		UserAgent:     s.UserAgent,
		MaxPerHost:    t.MaxPerHost,
		PreferPrimary: t.PreferPrimary,
		Logger:        s.Logger,
	})
	if err != nil {
		return nil, err
	}
	defer engine.Close()

	if err := runWithProgress(ctx, engine, progress); err != nil {
		return nil, err
	}

	s.Logger.Info("verifying", "path", engine.PartPath())
	report, err := verifyFile(ctx, engine.PartPath(), t, s)
	if err != nil {
		return nil, err
	}
	if report != nil {
		for _, c := range report.Checks {
			s.Logger.Info("check", "name", c.Name, "result", c.Result, "detail", c.Detail)
		}
	}
	if report != nil && report.Failed() {
		quarantine := t.Dest + ".unverified"
		if renameErr := os.Rename(engine.PartPath(), quarantine); renameErr == nil {
			_ = os.Remove(engine.StatePath())
			return nil, fmt.Errorf("%w: %s (kept as %s)", ErrUnverified, report.FailureReason(), quarantine)
		}
		return nil, fmt.Errorf("%w: %s", ErrUnverified, report.FailureReason())
	}

	if err := engine.Finalize(); err != nil {
		return nil, err
	}
	if report != nil {
		report.Path = t.Dest
	}
	return &Outcome{Path: t.Dest, Report: report}, nil
}

// checkExisting handles the case where the file is already sitting there: a
// verified copy is a result, not an obstacle, but a copy that fails its checks
// is left alone for the user to deal with.
func checkExisting(ctx context.Context, t Target, s Settings) (bool, *Outcome, error) {
	info, err := os.Stat(t.Dest)
	if err != nil {
		return false, nil, nil
	}
	if info.IsDir() {
		return true, nil, fmt.Errorf("%s is a directory", t.Dest)
	}

	report, err := verifyFile(ctx, t.Dest, t, s)
	if err != nil {
		return true, nil, err
	}
	if report != nil && report.Failed() {
		return true, nil, fmt.Errorf("%s already exists but %s — remove it to download again",
			t.Dest, report.FailureReason())
	}
	return true, &Outcome{Path: t.Dest, Report: report, AlreadyPresent: true}, nil
}

func verifyFile(ctx context.Context, path string, t Target, s Settings) (*verify.Report, error) {
	if s.SkipVerify || t.Verifier == nil {
		return nil, nil
	}
	return t.Verifier.Verify(ctx, path)
}

// runWithProgress drives the engine while sampling its counters for the UI.
func runWithProgress(ctx context.Context, engine *Engine, progress func(Snapshot)) error {
	if progress == nil {
		return engine.Run(ctx)
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				progress(engine.Snapshot()) // final frame, so the bar ends full
				return
			case <-ticker.C:
				progress(engine.Snapshot())
			}
		}
	}()

	err := engine.Run(ctx)
	close(done)
	<-stopped
	return err
}

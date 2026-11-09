package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
	"github.com/rs/zerolog"
	"github.com/schollz/progressbar/v3"
)

const progressEnvironmentVariable = "IPATOOL_PROGRESS"

type ProgressMode string

const (
	ProgressModeAuto  ProgressMode = "auto"
	ProgressModeBar   ProgressMode = "bar"
	ProgressModeJSON  ProgressMode = "json"
	ProgressModePlain ProgressMode = "plain"
	ProgressModeNone  ProgressMode = "none"
)

const (
	jsonProgressInterval   = 250 * time.Millisecond
	plainProgressHeartbeat = 15 * time.Second
	// Downloads lasting at least 3 seconds report every percent.
	plainProgressMinInterval = 3 * time.Second / 100
	plainProgressPercentStep = 1
)

type progressEnvironment struct {
	format           OutputFormat
	interactive      bool
	stdoutIsTerminal bool
}

// resolveProgressMode picks the progress mode from the flag, then the environment variable, then the session.
func resolveProgressMode(flagValue string, flagChanged bool, envValue string, env progressEnvironment) (ProgressMode, error) {
	value := flagValue
	if !flagChanged && envValue != "" {
		value = envValue
	}

	mode := ProgressMode(value)

	switch mode {
	case ProgressModeBar, ProgressModeJSON, ProgressModePlain, ProgressModeNone:
		return mode, nil
	case ProgressModeAuto:
		switch {
		// Existing JSON consumers may read stdout and stderr together, so JSON progress is opt-in.
		case env.format == OutputFormatJSON:
			return ProgressModeNone, nil
		case env.interactive && env.stdoutIsTerminal:
			return ProgressModeBar, nil
		default:
			return ProgressModePlain, nil
		}
	default:
		return "", fmt.Errorf("invalid progress mode '%s'; use auto, bar, json, plain, or none", value)
	}
}

func newProgress(mode ProgressMode, stdout, stderr io.Writer, now func() time.Time) appstore.Progress {
	switch mode {
	case ProgressModeBar:
		return &barProgress{out: stdout}
	case ProgressModeJSON:
		return &jsonProgress{logger: zerolog.New(stderr).With().Timestamp().Logger(), now: now}
	case ProgressModePlain:
		return &plainProgress{out: stderr, now: now}
	default:
		return nil
	}
}

type barProgress struct {
	out io.Writer
	bar *progressbar.ProgressBar
}

func (p *barProgress) Phase(phase appstore.Phase) {
	if phase == appstore.PhaseDownloading {
		p.bar = progressbar.NewOptions64(-1,
			progressbar.OptionSetDescription("downloading"),
			progressbar.OptionSetWriter(p.out),
			progressbar.OptionShowBytes(true),
			progressbar.OptionSetWidth(20),
			progressbar.OptionFullWidth(),
			progressbar.OptionThrottle(65*time.Millisecond),
			progressbar.OptionShowCount(),
			progressbar.OptionClearOnFinish(),
			progressbar.OptionSpinnerType(14),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionSetElapsedTime(false),
			progressbar.OptionSetPredictTime(false),
		)
	}
}

func (p *barProgress) Bytes(current, total int64) {
	if p.bar == nil {
		return
	}

	if p.bar.GetMax64() != total {
		p.bar.ChangeMax64(total)
	}

	_ = p.bar.Set64(current)
}

type jsonProgress struct {
	logger   zerolog.Logger
	now      func() time.Time
	phase    appstore.Phase
	lastEmit time.Time
}

func (p *jsonProgress) Phase(phase appstore.Phase) {
	if phase == p.phase {
		return
	}

	p.phase = phase
	p.lastEmit = time.Time{}
	p.logger.Info().Str("event", "progress").Str("phase", string(phase)).Send()
}

func (p *jsonProgress) Bytes(current, total int64) {
	now := p.now()
	finished := total >= 0 && current >= total

	if !finished && !p.lastEmit.IsZero() && now.Sub(p.lastEmit) < jsonProgressInterval {
		return
	}

	p.lastEmit = now

	event := p.logger.Info().Str("event", "progress").Str("phase", string(p.phase)).Int64("bytes", current)
	if total >= 0 {
		event = event.Int64("total", total)
	}

	event.Send()
}

// plainProgress prints one line per phase, then byte counts per percent and as a heartbeat while stalled.
type plainProgress struct {
	out         io.Writer
	now         func() time.Time
	phase       appstore.Phase
	lastEmit    time.Time
	lastPercent int64
}

func (p *plainProgress) Phase(phase appstore.Phase) {
	if phase == p.phase {
		return
	}

	p.phase = phase
	p.lastEmit = time.Time{}
	_, _ = fmt.Fprintln(p.out, phase)
}

func (p *plainProgress) Bytes(current, total int64) {
	now := p.now()

	var percent int64
	if total > 0 {
		percent = current * 100 / total
	}

	if !p.lastEmit.IsZero() {
		elapsed := now.Sub(p.lastEmit)
		advanced := total > 0 && percent-p.lastPercent >= plainProgressPercentStep

		if elapsed < plainProgressHeartbeat && (!advanced || elapsed < plainProgressMinInterval) {
			return
		}
	}

	p.lastEmit = now
	p.lastPercent = percent

	if total >= 0 {
		_, _ = fmt.Fprintf(p.out, "%s %d%% (%s / %s)\n", p.phase, percent, formatBytes(current), formatBytes(total))

		return
	}

	_, _ = fmt.Fprintf(p.out, "%s %s\n", p.phase, formatBytes(current))
}

func formatBytes(n int64) string {
	const unit = 1000

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	value := float64(n)
	suffixes := []string{"kB", "MB", "GB", "TB"}

	for _, suffix := range suffixes {
		value /= unit
		if value < unit || suffix == suffixes[len(suffixes)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}

	return fmt.Sprintf("%d B", n)
}

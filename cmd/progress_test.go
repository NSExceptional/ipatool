package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/majd/ipatool/v2/pkg/appstore"
	"github.com/majd/ipatool/v2/pkg/log"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

var _ = Describe("Progress", func() {
	DescribeTable("resolves the progress mode",
		func(flagValue string, flagChanged bool, envValue string, env progressEnvironment, expected ProgressMode) {
			mode, err := resolveProgressMode(flagValue, flagChanged, envValue, env)
			Expect(err).ToNot(HaveOccurred())
			Expect(mode).To(Equal(expected))
		},
		Entry("json output", "auto", false, "", progressEnvironment{format: OutputFormatJSON, interactive: true, stdoutIsTerminal: true}, ProgressModeNone),
		Entry("interactive terminal", "auto", false, "", progressEnvironment{interactive: true, stdoutIsTerminal: true}, ProgressModeBar),
		Entry("interactive without a terminal", "auto", false, "", progressEnvironment{interactive: true}, ProgressModePlain),
		Entry("non-interactive terminal", "auto", false, "", progressEnvironment{stdoutIsTerminal: true}, ProgressModePlain),
		Entry("environment variable", "auto", false, "none", progressEnvironment{}, ProgressModeNone),
		Entry("flag over environment variable", "json", true, "plain", progressEnvironment{}, ProgressModeJSON),
		Entry("auto from environment variable", "auto", false, "auto", progressEnvironment{interactive: true, stdoutIsTerminal: true}, ProgressModeBar),
		Entry("json from environment variable", "auto", false, "json", progressEnvironment{format: OutputFormatJSON}, ProgressModeJSON),
	)

	It("rejects unknown progress modes", func() {
		_, err := resolveProgressMode("fancy", true, "", progressEnvironment{})
		Expect(err).To(MatchError(ContainSubstring("invalid progress mode 'fancy'")))
	})

	It("disables progress for none", func() {
		Expect(newProgress(ProgressModeNone, nil, nil, time.Now)).To(BeNil())
	})

	Describe("plain", func() {
		var (
			clock    *fakeClock
			out      *bytes.Buffer
			progress appstore.Progress
		)

		BeforeEach(func() {
			clock = &fakeClock{now: time.Unix(0, 0)}
			out = &bytes.Buffer{}
			progress = newProgress(ProgressModePlain, nil, out, clock.Now)
		})

		It("prints phases once and throttles byte counts", func() {
			progress.Phase(appstore.PhaseResolving)
			progress.Phase(appstore.PhaseResolving)
			progress.Phase(appstore.PhaseDownloading)
			progress.Bytes(0, 300_000_000)

			clock.Advance(time.Second)
			progress.Bytes(150_000_000, 300_000_000)

			clock.Advance(15 * time.Second)
			progress.Bytes(150_000_000, 300_000_000)

			clock.Advance(15 * time.Second)
			progress.Bytes(160_000_000, 300_000_000)

			progress.Phase(appstore.PhasePatching)

			Expect(out.String()).To(Equal(strings.Join([]string{
				"resolving",
				"downloading",
				"downloading 0% (0 B / 300.0 MB)",
				"downloading 50% (150.0 MB / 300.0 MB)",
				"patching",
				"",
			}, "\n")))
		})

		It("prints byte counts on an interval when the total is unknown", func() {
			progress.Phase(appstore.PhaseDownloading)
			progress.Bytes(0, -1)

			clock.Advance(15 * time.Second)
			progress.Bytes(1_500, -1)

			Expect(out.String()).To(Equal("downloading\ndownloading 0 B\ndownloading 1.5 kB\n"))
		})
	})

	Describe("json", func() {
		var (
			clock    *fakeClock
			out      *bytes.Buffer
			progress appstore.Progress
		)

		events := func() []map[string]any {
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			result := make([]map[string]any, 0, len(lines))

			for _, line := range lines {
				var event map[string]any
				Expect(json.Unmarshal([]byte(line), &event)).To(Succeed())
				delete(event, "time")
				result = append(result, event)
			}

			return result
		}

		BeforeEach(func() {
			clock = &fakeClock{now: time.Unix(0, 0)}
			out = &bytes.Buffer{}
			progress = newProgress(ProgressModeJSON, nil, out, clock.Now)
		})

		It("emits phases and throttled byte counts", func() {
			progress.Phase(appstore.PhaseDownloading)
			progress.Bytes(0, 10)

			clock.Advance(100 * time.Millisecond)
			progress.Bytes(5, 10)

			clock.Advance(200 * time.Millisecond)
			progress.Bytes(6, 10)

			progress.Bytes(10, 10)
			progress.Phase(appstore.PhasePatching)

			Expect(events()).To(Equal([]map[string]any{
				{"level": "info", "event": "progress", "phase": "downloading"},
				{"level": "info", "event": "progress", "phase": "downloading", "bytes": float64(0), "total": float64(10)},
				{"level": "info", "event": "progress", "phase": "downloading", "bytes": float64(6), "total": float64(10)},
				{"level": "info", "event": "progress", "phase": "downloading", "bytes": float64(10), "total": float64(10)},
				{"level": "info", "event": "progress", "phase": "patching"},
			}))
		})

		It("omits unknown totals", func() {
			progress.Phase(appstore.PhaseDownloading)
			progress.Bytes(3, -1)

			Expect(events()[1]).To(Equal(map[string]any{"level": "info", "event": "progress", "phase": "downloading", "bytes": float64(3)}))
		})
	})

	DescribeTable("formats byte counts",
		func(n int64, expected string) {
			Expect(formatBytes(n)).To(Equal(expected))
		},
		Entry("bytes", int64(999), "999 B"),
		Entry("kilobytes", int64(1_500), "1.5 kB"),
		Entry("megabytes", int64(120_300_000), "120.3 MB"),
		Entry("gigabytes", int64(2_000_000_000), "2.0 GB"),
	)

	Describe("download command", func() {
		var stderr *bytes.Buffer

		BeforeEach(func() {
			previousDependencies := dependencies
			DeferCleanup(func() { dependencies = previousDependencies })
			dependencies.Logger = log.NewLogger(log.Args{})
			stderr = &bytes.Buffer{}
		})

		execute := func(args ...string) error {
			cmd := downloadCmdWithAppStore(func() appstore.AppStore { return &fakeDownloadAppStore{} })
			cmd.SetArgs(append([]string{"-i", "42"}, args...))
			cmd.SetErr(stderr)
			cmd.SetContext(context.WithValue(context.Background(), interactiveKey, false))

			return cmd.Execute()
		}

		It("reports phases in plain mode", func() {
			Expect(execute("--progress", "plain")).To(Succeed())
			Expect(stderr.String()).To(Equal("resolving\n"))
		})

		It("reads the mode from the environment", func() {
			Expect(os.Setenv(progressEnvironmentVariable, "none")).To(Succeed())
			DeferCleanup(os.Unsetenv, progressEnvironmentVariable)
			Expect(execute()).To(Succeed())
			Expect(stderr.String()).To(BeEmpty())
		})

		It("rejects an invalid mode", func() {
			Expect(execute("--progress", "fancy")).To(MatchError(ContainSubstring("invalid progress mode")))
		})
	})
})

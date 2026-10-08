package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/codegirl-007/jevlint/internal/evaluation"
	"github.com/codegirl-007/jevlint/internal/runner"
)

func attachRunTiming(
	report *runner.Report,
	start time.Time,
	timedRun bool,
	backend evaluation.BackendInfo,
) {
	if !timedRun {
		return
	}
	timing := evaluation.NewRunTiming(time.Since(start), backend)
	report.Timing = &timing
}

func writeRunTiming(writer io.Writer, timing *evaluation.RunTiming) {
	if timing == nil {
		return
	}
	fmt.Fprintf(
		writer,
		"  timing · %.2fs · %s · %s\n",
		timing.TotalSeconds,
		timing.Provider,
		timing.Model,
	)
}

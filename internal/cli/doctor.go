package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/codegirl-007/jevlint/internal/config"
	"github.com/codegirl-007/jevlint/internal/evaluation"
)

const doctorUsage = `Usage:
  jevlint doctor [flags]

Checks that the rule configuration loads and that the API credentials work.

Flags:
  --config path    rule configuration (default ".jevlint.json")
  --offline        skip the live request to the service
  --json           print machine-readable output
`

// doctorCheck is one diagnostic result.
type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// doctorReport is the machine-readable result of `jevlint doctor --json`.
type doctorReport struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
}

// executeDoctor checks the configuration and the API credentials.
func executeDoctor(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, doctorUsage) }
	configPath := flags.String("config", defaultConfigFile, "rule configuration")
	offline := flags.Bool("offline", false, "skip the live request")
	jsonOutput := flags.Bool("json", false, "print machine-readable output")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess
		}
		return exitUsageError
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "jevlint: doctor does not take arguments")
		return exitUsageError
	}

	checks := make([]doctorCheck, 0, 3)
	healthy := true

	absolute, migrationNote, err := prepareDoctorConfig(*configPath)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "config", Detail: err.Error()})
		healthy = false
	} else {
		if cfg, err := config.Load(absolute); err != nil {
			checks = append(checks, doctorCheck{Name: "config", Detail: err.Error()})
			healthy = false
		} else {
			detail := fmt.Sprintf(
				"%s (%d languages, %d rules)",
				absolute,
				len(cfg.Languages),
				len(cfg.Rules),
			)
			if migrationNote != "" {
				detail = migrationNote + "; " + detail
			}
			checks = append(checks, doctorCheck{
				Name: "config",
				OK:   true,
				Detail: detail,
			})
		}
	}

	client, err := evaluation.NewClientFromEnv(
		evaluation.Options{
			Logf:  debugLogger(stderr),
			Warnf: warnLogger(stderr),
		},
		os.Getenv,
	)
	if err != nil {
		checks = append(checks, doctorCheck{Name: "credentials", Detail: err.Error()})
		writeDoctorReport(stdout, stderr, checks, *jsonOutput)
		return exitUsageError
	}
	checks = append(checks, doctorCheck{
		Name:   "credentials",
		OK:     true,
		Detail: client.CredentialKind(),
	})

	switch {
	case *offline:
		checks = append(checks, doctorCheck{
			Name:   "service",
			OK:     true,
			Detail: "skipped (--offline)",
		})
	default:
		if err := client.Ping(ctx); err != nil {
			checks = append(checks, doctorCheck{Name: "service", Detail: err.Error()})
			healthy = false
		} else {
			checks = append(checks, doctorCheck{
				Name:   "service",
				OK:     true,
				Detail: fmt.Sprintf("%s · %s", client.Endpoint(), client.Model()),
			})
		}
	}

	writeDoctorReport(stdout, stderr, checks, *jsonOutput)
	if !healthy {
		return exitUsageError
	}
	return exitSuccess
}

// writeDoctorReport prints the checks as text or JSON.
func writeDoctorReport(
	stdout io.Writer,
	stderr io.Writer,
	checks []doctorCheck,
	asJSON bool,
) {
	if asJSON {
		report := doctorReport{OK: true, Checks: checks}
		for _, check := range checks {
			if !check.OK {
				report.OK = false
				break
			}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintf(stderr, "jevlint: write output: %v\n", err)
		}
		return
	}
	fmt.Fprintln(stdout, "jevlint doctor")
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "FAIL"
		}
		fmt.Fprintf(stdout, "  %-12s %-4s  %s\n", check.Name, status, check.Detail)
	}
}

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mixaill76/auto_ai_router/internal/spendcompare"
)

const (
	exitEqual    = 0
	exitDiverged = 1
	exitInvalid  = 2

	testDSNEnvironment      = "AIR_SPEND_TEST_DSN"
	referenceDSNEnvironment = "AIR_SPEND_REFERENCE_DSN"
)

type compareFunc func(context.Context, string, string, spendcompare.Filter) (spendcompare.Report, error)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, spendcompare.CompareDatabases))
}

func execute(args []string, stdout, stderr io.Writer, getenv func(string) string, compare compareFunc) int {
	options, err := parseOptions(args, getenv)
	if err != nil {
		writeError(stderr, err.Error())
		return exitInvalid
	}

	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()

	report, err := compare(ctx, options.testDSN, options.referenceDSN, options.filter)
	if err != nil {
		writeError(stderr, redact(err.Error(), options.testDSN, options.referenceDSN))
		return exitInvalid
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		writeError(stderr, "encode comparison report")
		return exitInvalid
	}
	if report.Equal {
		return exitEqual
	}
	return exitDiverged
}

type cliOptions struct {
	testDSN      string
	referenceDSN string
	filter       spendcompare.Filter
	timeout      time.Duration
}

func parseOptions(args []string, getenv func(string) string) (cliOptions, error) {
	flags := flag.NewFlagSet("spend-compare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var (
		fromValue, toValue    string
		testDSN, referenceDSN string
		requestID, callID     string
		timeout               time.Duration
	)
	flags.StringVar(&fromValue, "from", "", "inclusive UTC RFC3339 start")
	flags.StringVar(&toValue, "to", "", "exclusive UTC RFC3339 end")
	flags.StringVar(&testDSN, "test-dsn", "", "test PostgreSQL DSN (prefer AIR_SPEND_TEST_DSN)")
	flags.StringVar(&referenceDSN, "reference-dsn", "", "reference PostgreSQL DSN (prefer AIR_SPEND_REFERENCE_DSN)")
	flags.StringVar(&requestID, "request-id", "", "optional response/request ID")
	flags.StringVar(&callID, "call-id", "", "optional LiteLLM call ID")
	flags.DurationVar(&timeout, "timeout", time.Minute, "overall comparison timeout")
	if err := flags.Parse(args); err != nil {
		return cliOptions{}, fmt.Errorf("invalid arguments")
	}
	if flags.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected positional arguments")
	}
	if timeout <= 0 {
		return cliOptions{}, fmt.Errorf("--timeout must be positive")
	}

	from, err := spendcompare.ParseUTC(fromValue)
	if err != nil {
		return cliOptions{}, fmt.Errorf("invalid --from: %w", err)
	}
	to, err := spendcompare.ParseUTC(toValue)
	if err != nil {
		return cliOptions{}, fmt.Errorf("invalid --to: %w", err)
	}
	window, err := spendcompare.NewWindow(from, to)
	if err != nil {
		return cliOptions{}, err
	}

	if testDSN == "" {
		testDSN = getenv(testDSNEnvironment)
	}
	if referenceDSN == "" {
		referenceDSN = getenv(referenceDSNEnvironment)
	}
	if testDSN == "" {
		return cliOptions{}, fmt.Errorf("test DSN is required via --test-dsn or %s", testDSNEnvironment)
	}
	if referenceDSN == "" {
		return cliOptions{}, fmt.Errorf("reference DSN is required via --reference-dsn or %s", referenceDSNEnvironment)
	}

	return cliOptions{
		testDSN:      testDSN,
		referenceDSN: referenceDSN,
		filter: spendcompare.Filter{
			Window:    window,
			RequestID: requestID,
			CallID:    callID,
		},
		timeout: timeout,
	}, nil
}

func redact(message string, secrets ...string) string {
	redacted := message
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, secret, "[redacted]")
		parsed, err := url.Parse(secret)
		if err != nil || parsed.User == nil {
			continue
		}
		if password, ok := parsed.User.Password(); ok && password != "" {
			redacted = strings.ReplaceAll(redacted, password, "[redacted]")
			redacted = strings.ReplaceAll(redacted, url.QueryEscape(password), "[redacted]")
		}
	}
	return redacted
}

func writeError(output io.Writer, message string) {
	_ = json.NewEncoder(output).Encode(map[string]string{"error": message})
}

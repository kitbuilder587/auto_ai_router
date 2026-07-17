package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mixaill76/auto_ai_router/internal/shadowcompare"
)

func TestExecuteReturnsNonZeroOnDivergence(t *testing.T) {
	t.Parallel()

	compare := func(context.Context, string, string, shadowcompare.Filter) (shadowcompare.Report, error) {
		return shadowcompare.Report{Equal: false}, nil
	}

	var stdout, stderr bytes.Buffer
	code := execute(validArgs(), &stdout, &stderr, noEnvironment, compare)
	if code != exitDiverged {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitDiverged, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"equal": false`) {
		t.Fatalf("stdout = %q, want JSON report", stdout.String())
	}
}

func TestExecuteReturnsZeroOnEqualReport(t *testing.T) {
	t.Parallel()

	compare := func(context.Context, string, string, shadowcompare.Filter) (shadowcompare.Report, error) {
		return shadowcompare.Report{Equal: true}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := execute(validArgs(), &stdout, &stderr, noEnvironment, compare); code != exitEqual {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitEqual, stderr.String())
	}
}

func TestExecuteRequiresUTCWindowAndDSNs(t *testing.T) {
	t.Parallel()

	compare := func(context.Context, string, string, shadowcompare.Filter) (shadowcompare.Report, error) {
		t.Fatal("compare must not be called for invalid input")
		return shadowcompare.Report{}, nil
	}

	for name, args := range map[string][]string{
		"missing from": {"--to", "2026-07-12T09:00:00Z", "--test-dsn", "postgres://test", "--reference-dsn", "postgres://reference"},
		"non UTC":      {"--from", "2026-07-12T11:00:00+03:00", "--to", "2026-07-12T12:00:00+03:00", "--test-dsn", "postgres://test", "--reference-dsn", "postgres://reference"},
		"over 24h":     {"--from", "2026-07-11T08:00:00Z", "--to", "2026-07-12T08:00:01Z", "--test-dsn", "postgres://test", "--reference-dsn", "postgres://reference"},
		"missing DSNs": {"--from", "2026-07-12T08:00:00Z", "--to", "2026-07-12T09:00:00Z"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := execute(args, &stdout, &stderr, noEnvironment, compare); code != exitInvalid {
				t.Fatalf("exit code = %d, want %d", code, exitInvalid)
			}
		})
	}
}

func TestExecuteNeverPrintsDSNs(t *testing.T) {
	t.Parallel()

	testDSN := "postgres://alice:secret-test@example.invalid/test-db"
	referenceDSN := "postgres://bob:secret-reference@example.invalid/vsellm-db"
	compare := func(_ context.Context, gotTest, gotReference string, _ shadowcompare.Filter) (shadowcompare.Report, error) {
		if gotTest != testDSN || gotReference != referenceDSN {
			t.Fatalf("DSNs were not passed to comparer")
		}
		return shadowcompare.Report{}, errors.New("failed using " + gotTest + " and " + gotReference)
	}

	args := []string{
		"--from", "2026-07-12T08:00:00Z",
		"--to", "2026-07-12T09:00:00Z",
		"--test-dsn", testDSN,
		"--reference-dsn", referenceDSN,
	}
	var stdout, stderr bytes.Buffer
	if code := execute(args, &stdout, &stderr, noEnvironment, compare); code != exitInvalid {
		t.Fatalf("exit code = %d, want %d", code, exitInvalid)
	}
	combined := stdout.String() + stderr.String()
	for _, secret := range []string{testDSN, referenceDSN, "secret-test", "secret-reference"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("output leaked %q: %s", secret, combined)
		}
	}
}

func validArgs() []string {
	return []string{
		"--from", "2026-07-12T08:00:00Z",
		"--to", "2026-07-12T09:00:00Z",
		"--test-dsn", "postgres://test",
		"--reference-dsn", "postgres://reference",
	}
}

func noEnvironment(string) string { return "" }

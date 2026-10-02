package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMustEnvReturnsSetValue(t *testing.T) {
	t.Setenv("MUSTENV_TEST_VAR", "some-value")

	if got := mustEnv("MUSTENV_TEST_VAR"); got != "some-value" {
		t.Fatalf("mustEnv(%q) = %q, want %q", "MUSTENV_TEST_VAR", got, "some-value")
	}
}

// TestMustEnvFatalsWhenUnset verifies the missing-variable path by re-running
// this test binary in a subprocess: the child dies via log.Fatal before it
// can report a PASS, so the parent observes a non-zero exit status and the
// fatal message naming the variable.
func TestMustEnvFatalsWhenUnset(t *testing.T) {
	if os.Getenv("MUSTENV_CHILD") == "1" {
		mustEnv("MUSTENV_MISSING_VAR") //nolint:staticcheck // SA1020: process exits here by design
		return
	}

	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestMustEnvFatalsWhenUnset$")
	cmd.Env = append(os.Environ(), "MUSTENV_CHILD=1", "MUSTENV_MISSING_VAR=")
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("mustEnv on unset variable: want non-zero process exit, got err=%v", err)
	}
	if !strings.Contains(stderr.String(), "MUSTENV_MISSING_VAR environment variable is not set") {
		t.Fatalf("fatal message = %q, want it to name %q as not set", stderr.String(), "MUSTENV_MISSING_VAR")
	}
}

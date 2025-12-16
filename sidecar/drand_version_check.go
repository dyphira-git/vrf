package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

type DrandVersionCheckMode string

const (
	DrandVersionCheckStrict DrandVersionCheckMode = "strict"
	DrandVersionCheckOff    DrandVersionCheckMode = "off"
)

var (
	errInvalidDrandVersionCheckMode = errors.New("invalid drand version check mode (expected strict|off)")
	errDrandVersionOutputEmpty      = errors.New("drand version output is empty")
	errParseDrandSemverFailed       = errors.New("unable to parse semver from drand version output")
	errDrandVersionMismatch         = errors.New("drand version mismatch")
	errDrandCommitMismatch          = errors.New("drand commit mismatch")
	errMissingExpectedDrandSemver   = errors.New("drand version pinning is enabled but the sidecar build is missing an expected drand semver; rebuild with Makefile ldflags or run with --drand-version-check=off")
)

func ParseDrandVersionCheckMode(s string) (DrandVersionCheckMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(DrandVersionCheckStrict):
		return DrandVersionCheckStrict, nil
	case string(DrandVersionCheckOff):
		return DrandVersionCheckOff, nil
	default:
		return "", fmt.Errorf("%w: %q", errInvalidDrandVersionCheckMode, s)
	}
}

type discoveredDrandVersion struct {
	semver string
	commit string
	raw    string
}

var (
	drandSemverRe = regexp.MustCompile(`\bv?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?\b`)
	drandCommitRe = regexp.MustCompile(`(?i)\b(?:commit|revision|git)[\s:=]+([0-9a-f]{7,40})\b`)

	drandVersionOutputFunc = drandVersionOutput
)

func parseDrandVersionOutput(output string) (discoveredDrandVersion, error) {
	out := strings.TrimSpace(output)
	if out == "" {
		return discoveredDrandVersion{}, errDrandVersionOutputEmpty
	}

	semver := drandSemverRe.FindString(out)
	semver = strings.TrimPrefix(semver, "v")
	if semver == "" {
		return discoveredDrandVersion{}, fmt.Errorf("%w: %q", errParseDrandSemverFailed, out)
	}

	var commit string
	if m := drandCommitRe.FindStringSubmatch(out); len(m) == 2 {
		commit = strings.ToLower(m[1])
	}

	return discoveredDrandVersion{
		semver: semver,
		commit: commit,
		raw:    out,
	}, nil
}

func drandVersionOutput(ctx context.Context, drandPath string) (string, error) {
	out, err := exec.CommandContext(ctx, drandPath, "--version").CombinedOutput() //nolint:gosec
	if err != nil {
		legacyOut, legacyErr := exec.CommandContext(ctx, drandPath, "version").CombinedOutput() //nolint:gosec
		if legacyErr != nil {
			return "", fmt.Errorf(
				"running drand --version: %w: %s",
				err,
				strings.TrimSpace(string(out)),
			)
		}
		out = legacyOut
	}

	return string(out), nil
}

func checkDrandBinaryVersion(cfg Config, logger *zap.Logger) error {
	mode := cfg.DrandVersionCheck
	if mode == "" {
		mode = DrandVersionCheckStrict
	}

	if logger == nil {
		logger = zap.NewNop()
	}

	path := strings.TrimSpace(cfg.BinaryPath)
	if path == "" {
		path = "drand"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := drandVersionOutputFunc(ctx, path)
	if err != nil {
		if mode == DrandVersionCheckOff {
			logger.Warn("drand version check disabled; unable to probe drand binary", zap.Error(err))
			return nil
		}
		return err
	}

	discovered, parseErr := parseDrandVersionOutput(out)
	if parseErr != nil {
		if mode == DrandVersionCheckOff {
			logger.Warn("drand version check disabled; unable to parse drand version output", zap.Error(parseErr))
			return nil
		}
		return parseErr
	}

	logger.Info(
		"detected drand binary version",
		zap.String("semver", discovered.semver),
		zap.String("commit", discovered.commit),
	)

	expected := getExpectedDrandVersion()

	if mode == DrandVersionCheckOff {
		logger.Warn(
			"drand version check disabled; running without pinned drand semantics (development only)",
			zap.String("expected_semver", expected.semver),
			zap.String("expected_commit", expected.commit),
		)
		return nil
	}

	if expected.semver == "" {
		return errMissingExpectedDrandSemver
	}

	if discovered.semver != expected.semver {
		return fmt.Errorf("%w: got %q, expected %q", errDrandVersionMismatch, discovered.semver, expected.semver)
	}

	if expected.commit != "" {
		if discovered.commit == "" {
			return fmt.Errorf("%w: got <empty>, expected %q", errDrandCommitMismatch, expected.commit)
		}
		if discovered.commit != expected.commit {
			return fmt.Errorf("%w: got %q, expected %q", errDrandCommitMismatch, discovered.commit, expected.commit)
		}
	}

	return nil
}

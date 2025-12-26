package drand

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseDrandVersionOutput(t *testing.T) {
	tcs := []struct {
		name   string
		output string
		semver string
		commit string
	}{
		{
			name:   "plain-semver",
			output: "drand 2.2.0",
			semver: "2.2.0",
		},
		{
			name:   "leading-v",
			output: "drand version v2.3.1",
			semver: "2.3.1",
		},
		{
			name:   "with-commit",
			output: "drand 2.2.0 (commit: deadBEEF)",
			semver: "2.2.0",
			commit: "deadbeef",
		},
		{
			name:   "git-label",
			output: "drand v2.1.0 git 0123abcd",
			semver: "2.1.0",
			commit: "0123abcd",
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDrandVersionOutput(tc.output)
			require.NoError(t, err)
			require.Equal(t, tc.semver, got.semver)
			require.Equal(t, tc.commit, got.commit)
		})
	}

	_, err := parseDrandVersionOutput("no version here")
	require.Error(t, err)
}

func TestCheckDrandBinaryVersion_Strict(t *testing.T) {
	oldOutputFn := drandVersionOutputFunc
	oldSemver := expectedDrandSemver
	oldCommit := expectedDrandCommit
	t.Cleanup(func() {
		drandVersionOutputFunc = oldOutputFn
		expectedDrandSemver = oldSemver
		expectedDrandCommit = oldCommit
	})

	expectedDrandSemver = "2.2.0"
	expectedDrandCommit = ""

	drandVersionOutputFunc = func(context.Context, string) (string, error) {
		return "drand 2.2.0 (commit: deadbeef)", nil
	}

	err := checkDrandBinaryVersion(Config{
		BinaryPath:        "drand-mock",
		DrandVersionCheck: DrandVersionCheckStrict,
	}, zap.NewNop())
	require.NoError(t, err)
}

func TestCheckDrandBinaryVersion_StrictMismatch(t *testing.T) {
	oldOutputFn := drandVersionOutputFunc
	oldSemver := expectedDrandSemver
	oldCommit := expectedDrandCommit
	t.Cleanup(func() {
		drandVersionOutputFunc = oldOutputFn
		expectedDrandSemver = oldSemver
		expectedDrandCommit = oldCommit
	})

	expectedDrandSemver = "2.2.0"
	expectedDrandCommit = ""

	drandVersionOutputFunc = func(context.Context, string) (string, error) {
		return "drand 2.1.9", nil
	}

	err := checkDrandBinaryVersion(Config{
		BinaryPath:        "drand-mock",
		DrandVersionCheck: DrandVersionCheckStrict,
	}, zap.NewNop())
	require.Error(t, err)
}

func TestCheckDrandBinaryVersion_MissingExpectedSemverDoesNotFail(t *testing.T) {
	oldOutputFn := drandVersionOutputFunc
	oldSemver := expectedDrandSemver
	oldCommit := expectedDrandCommit
	t.Cleanup(func() { drandVersionOutputFunc = oldOutputFn })
	t.Cleanup(func() {
		expectedDrandSemver = oldSemver
		expectedDrandCommit = oldCommit
	})

	expectedDrandSemver = ""
	expectedDrandCommit = ""

	drandVersionOutputFunc = func(context.Context, string) (string, error) {
		return "drand 2.2.0", nil
	}

	err := checkDrandBinaryVersion(Config{
		BinaryPath:        "drand-mock",
		DrandVersionCheck: DrandVersionCheckStrict,
	}, zap.NewNop())
	require.NoError(t, err)
}

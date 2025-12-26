package main

import (
	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sidecar",
		Short:         "VRF sidecar (supervises drand + serves randomness over gRPC/HTTP)",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newStartCmd(),
		newInitCmd(),
		newVersionCmd(),
		newConfigCmd(),
		newDrandCmd(),
	)

	return cmd
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "start",
		Short:              "Start the sidecar server (and supervise a drand subprocess)",
		DisableFlagParsing: true, // preserve existing `flag`-based parsing and `--help` output
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runStart(cmd.Context(), args)
			if code == 0 {
				return nil
			}
			return &exitCodeError{code: code}
		},
	}
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "init",
		Short:              "Initialize <chain_home>/config/vrf.toml",
		DisableFlagParsing: true, // preserve existing `flag`-based parsing and `--help` output
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := runInit(args)
			if code == 0 {
				return nil
			}
			return &exitCodeError{code: code}
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			printVersion(cmd.OutOrStdout())
			return nil
		},
	}
}

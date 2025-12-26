package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
)

func newDrandCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "drand",
		Short:         "Manage a supervised drand subprocess via the sidecar",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	root.PersistentFlags().String("addr", "", "sidecar gRPC address (unix://... or host:port)")

	_ = v.BindPFlag("addr", root.PersistentFlags().Lookup("addr"))
	_ = v.BindEnv("addr", "SIDECAR_LISTEN_ADDR")

	root.AddCommand(newDrandConfigCmd())
	root.AddCommand(newDrandStartCmd(v))
	root.AddCommand(newDrandStopCmd(v))
	root.AddCommand(newDrandDKGCmd(v))
	root.AddCommand(newDrandReshareCmd(v))
	root.AddCommand(newDrandLogsCmd(v))

	return root
}

func newDrandConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the drand config file location",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := defaultDrandConfigPath()
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("unable to determine config path (set --drand-config on start, or VRF_CONFIG for config subcommands)")
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func newDrandStartCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the supervised drand subprocess",
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr := defaultSidecarAddr(v.GetString("addr"))
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			conn, err := dialSidecar(ctx, addr)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			c := sidecarv1.NewDrandControlClient(conn)
			res, err := c.Start(ctx, &sidecarv1.DrandStartRequest{})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running=%t pid=%d\n", res.GetRunning(), res.GetPid())
			return nil
		},
	}
}

func newDrandStopCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the supervised drand subprocess",
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr := defaultSidecarAddr(v.GetString("addr"))
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			conn, err := dialSidecar(ctx, addr)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			c := sidecarv1.NewDrandControlClient(conn)
			res, err := c.Stop(ctx, &sidecarv1.DrandStopRequest{})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "running=%t\n", res.GetRunning())
			return nil
		},
	}
}

func newDrandLogsCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show recent drand logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr := defaultSidecarAddr(v.GetString("addr"))
			tail, _ := cmd.Flags().GetInt("tail")
			if tail <= 0 {
				tail = 200
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			conn, err := dialSidecar(ctx, addr)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()

			c := sidecarv1.NewDrandControlClient(conn)
			res, err := c.Logs(ctx, &sidecarv1.DrandLogsRequest{Tail: uint32(tail)})
			if err != nil {
				return err
			}

			for _, e := range res.GetEntries() {
				line := strings.TrimRight(e.GetLine(), "\n")
				switch e.GetStream() {
				case "", "stdout", "unknown":
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), line)
				default:
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n", e.GetStream(), line)
				}
			}

			return nil
		},
	}

	cmd.Flags().Int("tail", 200, "max number of log lines to return")
	return cmd
}

func newDrandDKGCmd(_ *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "dkg",
		Short: "Manage drand DKG (not implemented yet)",
		Long: strings.TrimSpace(`
Manage drand DKG operations (placeholder).

Planned subcommands:
  - start / stop
  - dkg
  - reshare

This will be implemented via drand's gRPC control/DKG APIs (IPC), not by shelling out to "drand dkg ...".
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
}

func newDrandReshareCmd(_ *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "reshare",
		Short: "Manage drand resharing (not implemented yet)",
		Long: strings.TrimSpace(`
Manage drand resharing operations (placeholder).

This will be implemented via drand's gRPC control/DKG APIs (IPC), not by shelling out to "drand share --reshare".
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
}

func defaultSidecarAddr(v string) string {
	v = strings.TrimSpace(v)
	if v != "" {
		return v
	}
	return "unix:///var/run/vrf/sidecar.sock"
}

func dialSidecar(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("sidecar addr is empty")
	}

	isUnix := strings.HasPrefix(addr, "unix://")
	dialer := func(ctx context.Context, target string) (net.Conn, error) {
		if isUnix {
			path := strings.TrimPrefix(addr, "unix://")
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}

	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

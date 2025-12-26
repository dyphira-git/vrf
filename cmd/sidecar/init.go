package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

func runInit(args []string) int {
	fs := flag.NewFlagSet("sidecar init", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)

	reset := fs.Bool("reset", false, "reset an existing <chain_home>/config/vrf.toml to defaults (asks for confirmation)")

	drandHTTP := fs.String("drand-http", "", "drand HTTP base URL (defaults to http://<drand-public-addr>)")
	drandAllowNonLoop := fs.Bool("drand-allow-non-loopback-http", false, "allow drand-http to point at a non-loopback host (unsafe; intended for containerized dev)")
	drandPublic := fs.String("drand-public-addr", "127.0.0.1:8081", "drand public listen address (also used for HTTP)")
	drandPrivate := fs.String("drand-private-addr", "0.0.0.0:4444", "drand private listen address")
	drandControl := fs.String("drand-control-addr", "127.0.0.1:8888", "drand control listen address")
	drandDataDir := fs.String("drand-data-dir", "", "drand data directory (default: <chain_home>/drand)")
	drandID := fs.String("drand-id", "default", "drand beacon ID (default \"default\")")
	drandBinary := fs.String("drand-binary", "drand", "path to drand binary")
	drandNoRestart := fs.Bool("drand-no-restart", false, "do not restart drand subprocess after it exits (debugging)")
	drandRestartMin := fs.Duration("drand-restart-backoff-min", 1*time.Second, "minimum delay before restarting drand after exit")
	drandRestartMax := fs.Duration("drand-restart-backoff-max", 30*time.Second, "maximum delay between drand restart attempts")

	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "Usage:")
		_, _ = fmt.Fprintln(fs.Output(), "  sidecar init [chain_home] [flags]")
		_, _ = fmt.Fprintln(fs.Output(), "")
		_, _ = fmt.Fprintln(fs.Output(), "Flags:")
		fs.PrintDefaults()
	}

	usageToStderr := func() {
		out := fs.Output()
		fs.SetOutput(os.Stderr)
		fs.Usage()
		fs.SetOutput(out)
	}

	if err := fs.Parse(normalizeInitArgs(args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		usageToStderr()
		return 2
	}

	if fs.NArg() != 1 {
		_, _ = fmt.Fprintf(os.Stderr, "Error: accepts 1 arg(s), received %d\n", fs.NArg())
		usageToStderr()
		return 2
	}

	chainHome := strings.TrimSpace(fs.Arg(0))
	if chainHome == "" {
		_, _ = fmt.Fprintln(os.Stderr, "Error: chain_home must not be empty")
		usageToStderr()
		return 2
	}

	configDir := filepath.Join(chainHome, "config")
	configPath := filepath.Join(configDir, "vrf.toml")

	if _, err := os.Stat(configPath); err == nil {
		if !*reset {
			_, _ = fmt.Fprintf(os.Stderr, "vrf.toml already exists at %q (use --reset to reset it to defaults)\n", configPath)
			return 1
		}

		ok, confirmErr := confirmResetVrfToml(configPath)
		if confirmErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, confirmErr)
			return 1
		}
		if !ok {
			_, _ = fmt.Fprintln(os.Stderr, "aborted")
			return 1
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if *reset {
			_, _ = fmt.Fprintf(os.Stderr, "vrf.toml does not exist at %q (run init without --reset first)\n", configPath)
			return 1
		}
	} else {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "creating %s: %v\n", configDir, err)
		return 1
	}

	dataDir := strings.TrimSpace(*drandDataDir)
	if dataDir == "" {
		dataDir = filepath.Join(chainHome, "drand")
	}

	allowNonLoop := *drandAllowNonLoop
	noRestart := *drandNoRestart
	allowPublicBind := false
	dkgBeaconID := strings.TrimSpace(*drandID)
	if dkgBeaconID == "" {
		dkgBeaconID = "default"
	}
	cfg := drandFileConfig{
		HTTP:                 strings.TrimSpace(*drandHTTP),
		AllowNonLoopbackHTTP: &allowNonLoop,
		AllowPublicBind:      &allowPublicBind,
		PublicAddr:           strings.TrimSpace(*drandPublic),
		PrivateAddr:          strings.TrimSpace(*drandPrivate),
		ControlAddr:          strings.TrimSpace(*drandControl),
		DataDir:              dataDir,
		Binary:               strings.TrimSpace(*drandBinary),
		ID:                   strings.TrimSpace(*drandID),
		NoRestart:            &noRestart,
		RestartBackoffMin:    (*drandRestartMin).String(),
		RestartBackoffMax:    (*drandRestartMax).String(),
		DKGBeaconID:          dkgBeaconID,
		DKGThreshold:         0,
		DKGPeriodSeconds:     0,
		DKGGenesisUnix:       0,
		DKGTimeout:           "24h",
		DKGCatchupSeconds:    0,
		DKGJoinerAddrs:       []string{},
		GroupSourceAddr:      "",
		GroupSourceToken:     "",
	}

	var buf bytes.Buffer
	buf.WriteString("# VRF sidecar configuration (drand runtime wiring + DKG settings).\n")
	buf.WriteString("#\n")
	buf.WriteString("# The sidecar always supervises a local drand subprocess.\n")
	buf.WriteString("# Location: <chain_home>/config/vrf.toml\n")
	buf.WriteString("# Example:  ~/.chaind/config/vrf.toml\n\n")
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "encoding config: %v\n", err)
		return 1
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "writing %s: %v\n", tmpPath, err)
		return 1
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		_, _ = fmt.Fprintf(os.Stderr, "renaming %s -> %s: %v\n", tmpPath, configPath, err)
		return 1
	}

	_, _ = fmt.Fprintln(os.Stdout, configPath)
	return 0
}

func confirmResetVrfToml(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("empty config path")
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		_, _ = fmt.Fprintf(os.Stderr, "Reset %q to defaults? [y/N]: ", path)

		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}

		input := strings.TrimSpace(strings.ToLower(line))
		switch input {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(os.Stderr, "please answer y or n")
			if errors.Is(err, io.EOF) {
				return false, nil
			}
		}
	}
}

func normalizeInitArgs(args []string) []string {
	var flags []string
	var positionals []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}

		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)

		if initFlagTakesValue(arg) && !strings.Contains(arg, "=") && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}

	return append(flags, positionals...)
}

func initFlagTakesValue(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		name = name[:idx]
	}

	switch name {
	case "reset", "drand-allow-non-loopback-http", "drand-no-restart", "help", "h":
		return false
	default:
		return true
	}
}

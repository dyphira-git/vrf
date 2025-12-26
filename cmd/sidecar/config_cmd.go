package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/creachadair/tomledit"
	"github.com/creachadair/tomledit/parser"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/vexxvakan/vrf/sidecar/drand"
)

func newConfigCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "config",
		Short:         "Manage the sidecar vrf.toml configuration file",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	v := viper.New()
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))

	root.PersistentFlags().String("file", "", "path to vrf.toml (overrides defaults when set)")

	_ = v.BindPFlag("file", root.PersistentFlags().Lookup("file"))
	_ = v.BindEnv("file", "VRF_CONFIG")

	root.AddCommand(newConfigPathCmd(v))
	root.AddCommand(newConfigViewCmd(v))
	root.AddCommand(newConfigValidateCmd(v))
	root.AddCommand(newConfigGetCmd(v))
	root.AddCommand(newConfigSetCmd(v))

	return root
}

func newConfigPathCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the vrf.toml config file location",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveVrfConfigPath(v)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func newConfigViewCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "view",
		Short: "Print the current vrf.toml contents",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveVrfConfigPath(v)
			if err != nil {
				return err
			}

			b, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("reading %q: %w", path, err)
			}

			_, _ = cmd.OutOrStdout().Write(b)
			if len(b) > 0 && b[len(b)-1] != '\n' {
				_, _ = fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}
}

func newConfigValidateCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the vrf.toml configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveVrfConfigPath(v)
			if err != nil {
				return err
			}

			b, err := os.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					if home, ok := inferChainHomeFromVrfPath(path); ok {
						return fmt.Errorf("vrf.toml not found at %q (run `sidecar init %s` to create it)", path, home)
					}
					return fmt.Errorf("vrf.toml not found at %q (run `sidecar init <chain_home>` to create it)", path)
				}
				return fmt.Errorf("reading %q: %w", path, err)
			}

			if err := validateVrfTomlBytes(b); err != nil {
				return err
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
}

func newConfigGetCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "get [key]",
		Short: "Get a vrf.toml configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveVrfConfigPath(v)
			if err != nil {
				return err
			}

			doc, _, err := loadTomlDocument(path)
			if err != nil {
				return err
			}

			key, err := parser.ParseKey(args[0])
			if err != nil {
				return fmt.Errorf("invalid key %q: %w", args[0], err)
			}

			ent, err := findUniqueTomlMapping(doc, key)
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), ent.Value.String())
			return nil
		},
	}
}

func newConfigSetCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set [key] [value]",
		Short: "Set a vrf.toml configuration value (preserves formatting/comments)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveVrfConfigPath(v)
			if err != nil {
				return err
			}

			stdout, _ := cmd.Flags().GetBool("stdout")

			doc, perm, err := loadTomlDocument(path)
			if err != nil {
				return err
			}

			key, err := parser.ParseKey(args[0])
			if err != nil {
				return fmt.Errorf("invalid key %q: %w", args[0], err)
			}
			val, err := parseTomlValue(args[1])
			if err != nil {
				return fmt.Errorf("invalid value %q: %w", args[1], err)
			}

			ent, err := findUniqueTomlMapping(doc, key)
			if err != nil {
				return err
			}

			ent.KeyValue.Value = val

			data, err := formatTomlDocument(doc)
			if err != nil {
				return err
			}

			if err := validateVrfTomlBytes(data); err != nil {
				return err
			}

			if stdout {
				_, _ = cmd.OutOrStdout().Write(data)
				return nil
			}

			return writeFileAtomically(path, data, perm)
		},
	}

	cmd.Flags().Bool("stdout", false, "print the updated config to stdout (do not write the file)")
	return cmd
}

func resolveVrfConfigPath(v *viper.Viper) (string, error) {
	if v == nil {
		return "", fmt.Errorf("nil viper config")
	}

	if file := strings.TrimSpace(v.GetString("file")); file != "" {
		return file, nil
	}

	path := defaultDrandConfigPath()
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("unable to determine config path (set --file or VRF_CONFIG)")
	}
	return path, nil
}

func inferChainHomeFromVrfPath(path string) (string, bool) {
	path = filepath.Clean(strings.TrimSpace(path))
	if filepath.Base(path) != "vrf.toml" {
		return "", false
	}

	configDir := filepath.Dir(path)
	if filepath.Base(configDir) != "config" {
		return "", false
	}

	home := strings.TrimSpace(filepath.Dir(configDir))
	if home == "" || home == "." {
		return "", false
	}
	return home, true
}

func loadTomlDocument(path string) (*tomledit.Document, os.FileMode, error) {
	if strings.TrimSpace(path) == "" {
		return nil, 0, fmt.Errorf("empty config path")
	}

	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat %q: %w", path, err)
	}
	perm := st.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	doc, err := tomledit.Parse(f)
	if err != nil {
		return nil, 0, fmt.Errorf("parse %q: %w", path, err)
	}
	return doc, perm, nil
}

func findUniqueTomlMapping(doc *tomledit.Document, key parser.Key) (*tomledit.Entry, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil toml document")
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("empty key")
	}

	results := doc.Find(key...)
	if len(results) == 0 {
		return nil, fmt.Errorf("key %q not found", key.String())
	}
	if len(results) > 1 {
		return nil, fmt.Errorf("key %q is ambiguous (%d matches)", key.String(), len(results))
	}
	if results[0].IsSection() {
		return nil, fmt.Errorf("key %q refers to a table, not a value", key.String())
	}
	return results[0], nil
}

func parseTomlValue(raw string) (parser.Value, error) {
	if v, err := parser.ParseValue(raw); err == nil {
		return v, nil
	}
	return parser.ParseValue(strconv.Quote(raw))
}

func formatTomlDocument(doc *tomledit.Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil toml document")
	}

	var buf bytes.Buffer
	if err := tomledit.Format(&buf, doc); err != nil {
		return nil, fmt.Errorf("formatting toml: %w", err)
	}
	return buf.Bytes(), nil
}

func writeFileAtomically(path string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path")
	}
	if perm == 0 {
		perm = 0o644
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("writing %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %q -> %q: %w", tmp, path, err)
	}
	return nil
}

func validateVrfTomlBytes(data []byte) error {
	var fileCfg drandFileConfig
	md, err := toml.Decode(string(data), &fileCfg)
	if err != nil {
		return fmt.Errorf("parsing vrf.toml: %w", err)
	}
	if md.IsDefined("supervise") {
		return fmt.Errorf("unsupported vrf.toml key %q: drand supervision is always enabled; remove the key", "supervise")
	}
	if md.IsDefined("version_check") {
		return fmt.Errorf("unsupported vrf.toml key %q: drand version checks are always enforced; remove the key", "version_check")
	}

	allowNonLoopback := fileCfg.allowNonLoopbackOr(false)

	restartMin, err := fileCfg.restartBackoffMinOr(1 * time.Second)
	if err != nil {
		return fmt.Errorf("invalid restart_backoff_min: %w", err)
	}
	restartMax, err := fileCfg.restartBackoffMaxOr(30 * time.Second)
	if err != nil {
		return fmt.Errorf("invalid restart_backoff_max: %w", err)
	}

	if _, err := fileCfg.dkgTimeoutOr(0); err != nil {
		return fmt.Errorf("invalid dkg_timeout: %w", err)
	}

	versionMode := drand.DrandVersionCheckStrict

	drandCfg := drand.Config{
		DrandHTTP:                 fileCfg.httpOr(""),
		DrandAllowNonLoopbackHTTP: allowNonLoopback,
		BinaryPath:                fileCfg.binaryOr("drand"),
		DrandVersionCheck:         versionMode,
		DrandDataDir:              fileCfg.dataDirOr(""),
		DrandID:                   fileCfg.idOr("default"),
		DrandPrivateListen:        fileCfg.privateAddrOr("0.0.0.0:4444"),
		DrandPublicListen:         fileCfg.publicAddrOr("127.0.0.1:8081"),
		DrandControlListen:        fileCfg.controlAddrOr("127.0.0.1:8888"),
	}
	if strings.TrimSpace(drandCfg.DrandHTTP) == "" {
		drandCfg.DrandHTTP = "http://" + strings.TrimSpace(drandCfg.DrandPublicListen)
	}

	if err := drandCfg.ValidateBasic(); err != nil {
		return err
	}

	procCfg := drand.DrandProcessConfig{
		BinaryPath:        drandCfg.BinaryPath,
		DataDir:           drandCfg.DrandDataDir,
		ID:                drandCfg.DrandID,
		PrivateListen:     drandCfg.DrandPrivateListen,
		PublicListen:      drandCfg.DrandPublicListen,
		ControlListen:     drandCfg.DrandControlListen,
		DisableRestart:    fileCfg.noRestartOr(false),
		RestartBackoffMin: restartMin,
		RestartBackoffMax: restartMax,
	}
	if err := procCfg.ValidateBasic(); err != nil {
		return err
	}

	return nil
}

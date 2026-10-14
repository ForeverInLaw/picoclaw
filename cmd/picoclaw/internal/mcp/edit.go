package mcp

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.mau.fi/util/shlex"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
)

var editorCommand = exec.CommandContext

func newEditCommand() *cobra.Command {
	return &cobra.Command{
		Use: "edit", Short: "Edit a temporary configuration with $VISUAL or $EDITOR", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			editor := strings.TrimSpace(os.Getenv("VISUAL"))
			if editor == "" {
				editor = strings.TrimSpace(os.Getenv("EDITOR"))
			}
			if editor == "" {
				return fmt.Errorf("set VISUAL or EDITOR before editing MCP configuration")
			}
			args, err := shlex.Split(editor)
			if err != nil || len(args) == 0 {
				return fmt.Errorf("invalid VISUAL/EDITOR command")
			}
			path := internal.GetConfigPath()
			original, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("could not read original configuration")
			}
			originalExists := err == nil
			securityPath := filepath.Join(filepath.Dir(path), config.SecurityConfigFile)
			security, securityErr := os.ReadFile(securityPath)
			if securityErr != nil && !os.IsNotExist(securityErr) {
				return fmt.Errorf("could not read original security settings")
			}
			securityExists := securityErr == nil
			dir, err := os.MkdirTemp("", "picoclaw-mcp-edit-")
			if err != nil {
				return fmt.Errorf("could not create secure editor temporary directory")
			}
			defer os.RemoveAll(dir)
			temp := filepath.Join(dir, "config.json")
			if err = config.PrepareConfig(path, temp); err != nil {
				return fmt.Errorf("could not prepare configuration for editor; original unchanged")
			}
			process := editorCommand(cmd.Context(), args[0], append(args[1:], temp)...)
			process.Stdin, process.Stdout, process.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err = process.Run(); err != nil {
				return fmt.Errorf("editor failed; original configuration unchanged")
			}
			edited, err := config.LoadConfig(temp)
			if err != nil {
				return fmt.Errorf("edited configuration is invalid; original unchanged")
			}
			if err = edited.Tools.MCP.Validate(); err != nil {
				return fmt.Errorf("edited configuration is invalid: %w", err)
			}
			current, readErr := os.ReadFile(path)
			if readErr != nil && !os.IsNotExist(readErr) {
				return fmt.Errorf("could not recheck original configuration")
			}
			currentSecurity, securityErr := os.ReadFile(securityPath)
			if securityErr != nil && !os.IsNotExist(securityErr) {
				return fmt.Errorf("could not recheck original security settings")
			}
			if !bytes.Equal(current, original) || originalExists != (readErr == nil) ||
				!bytes.Equal(currentSecurity, security) || securityExists != (securityErr == nil) {
				return fmt.Errorf("configuration changed while editing; original unchanged")
			}
			if err = saveConfig(edited); err != nil {
				return err
			}
			cmd.Println("MCP configuration saved.")
			return nil
		},
	}
}

package migrate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/migrate"
)

func NewMigrateCommand() *cobra.Command {
	var opts migrate.Options
	var configSource, configOutput string

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate from xxxclaw(openclaw, etc.) to picoclaw",
		Args:  cobra.NoArgs,
		Example: `  picoclaw migrate
  picoclaw migrate --from openclaw
  picoclaw migrate --dry-run
  picoclaw migrate --refresh
  picoclaw migrate --force`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if configSource != "" {
				if configOutput == "" {
					return fmt.Errorf("--output is required with --config; the original config is preserved")
				}
				source, err := filepath.Abs(configSource)
				if err != nil {
					return err
				}
				output, err := filepath.Abs(configOutput)
				if err != nil {
					return err
				}
				if source == output {
					return fmt.Errorf("--output must differ from --config")
				}
				if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
					return fmt.Errorf("--output must be a new file")
				}
				if err := config.PrepareConfig(source, output); err != nil {
					return err
				}
				cmd.Printf("Prepared v3 config: %s\n", output)
				return nil
			}
			if configOutput != "" {
				return fmt.Errorf("--output requires --config")
			}
			m := migrate.NewMigrateInstance(opts)
			result, err := m.Run(opts)
			if err != nil {
				return err
			}
			if !opts.DryRun {
				m.PrintSummary(result)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false,
		"Show what would be migrated without making changes")
	cmd.Flags().StringVar(&configSource, "config", "", "Prepare this legacy/current PicoClaw configuration as v3")
	cmd.Flags().
		StringVar(&configOutput, "output", "", "Write prepared v3 configuration to this new path, preserving the source")
	cmd.Flags().StringVar(&opts.Source, "from", "openclaw",
		"Source to migrate from (e.g., openclaw)")
	cmd.Flags().BoolVar(&opts.Refresh, "refresh", false,
		"Re-sync workspace files from OpenClaw (repeatable)")
	cmd.Flags().BoolVar(&opts.ConfigOnly, "config-only", false,
		"Only migrate config, skip workspace files")
	cmd.Flags().BoolVar(&opts.WorkspaceOnly, "workspace-only", false,
		"Only migrate workspace files, skip config")
	cmd.Flags().BoolVar(&opts.Force, "force", false,
		"Skip confirmation prompts")
	cmd.Flags().StringVar(&opts.SourceHome, "source-home", "",
		"Override source home directory (default: ~/.openclaw)")
	cmd.Flags().StringVar(&opts.TargetHome, "target-home", "",
		"Override target home directory (default: ~/.picoclaw)")

	return cmd
}

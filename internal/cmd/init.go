package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initForce bool

const defaultNovaHCL = `variable "project_name" {
  default = "my-project"
}

vm {
  name   = var.project_name
  image  = "ghcr.io/3clabs/ubuntu-cloud:24.04"
  cpus   = 2
  memory = "2G"

  port_forward {
    host  = 8080
    guest = 80
  }

  shared_folder {
    host_path  = "."
    guest_path = "/workspace"
  }
}
`

const defaultCloudConfig = `#cloud-config
package_update: true
packages:
  - curl
  - git
`

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a default nova.hcl configuration file",
		RunE:  runInit,
	}
	cmd.Flags().BoolVarP(&initForce, "force", "f", false, "overwrite existing files")
	return cmd
}

func runInit(cmd *cobra.Command, args []string) error {
	files := map[string]string{
		"nova.hcl":          defaultNovaHCL,
		"cloud-config.yaml": defaultCloudConfig,
	}

	for name, content := range files {
		path := filepath.Join(".", name)
		if !initForce {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", name)
			}
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
		slog.Info("created", "file", name)
	}

	fmt.Println("Nova project initialized. Edit nova.hcl to configure your VM.")
	return nil
}

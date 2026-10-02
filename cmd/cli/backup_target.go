package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	sdspb "github.com/haify-project/sds/api/proto/v1"
)

// secretEnvVar is where `backup target add` reads the object-store secret from
// by default.
//
// There is deliberately no --secret-key flag. A secret passed as a flag lands in
// the operator's shell history and in the argv of this process, visible to every
// other user on the machine for as long as the command runs — which defeats the
// care taken on the rest of the path, where the secret never enters a command
// line on the controller or on any node.
const secretEnvVar = "SDS_BACKUP_SECRET"

func backupTargetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage backup repositories (S3 / SMB / WebDAV)",
	}
	cmd.AddCommand(backupTargetAddCommand())
	cmd.AddCommand(backupTargetListCommand())
	cmd.AddCommand(backupTargetDeleteCommand())
	return cmd
}

func backupTargetAddCommand() *cobra.Command {
	var name, kind, prefix string
	var bucket, endpoint, region string
	var host, share string
	var user, secretFile string
	var secretObscured bool

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add or replace a backup target",
		Long: `Define where backups are shipped.

Adding a target that already exists replaces it, which is how credentials are
rotated.

THE SECRET IS NEVER A FLAG. It is read from the ` + secretEnvVar + ` environment
variable, or from --secret-file (use "-" for stdin). A flag would put the key in
your shell history and in this process's argv; from here on it only ever travels
inside the controller's 0600 database and, on the node that runs the transfer, a
0600 file written over the SSH stream. No RPC ever returns it.

Examples:
  # S3-compatible object store
  export ` + secretEnvVar + `='...'
  sds backup target add --name offsite --kind s3 \
      --bucket sds-backups --endpoint https://s3.example.com --user AKIAEXAMPLE

  # SMB share on a NAS (the password is obscured for rclone automatically;
  # pass --secret-obscured if you would rather run 'rclone obscure' yourself)
  sds backup target add --name nas --kind smb \
      --host nas.lan --share backups --user backupuser --secret-file -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if kind == "" {
				return fmt.Errorf("--kind is required (s3, smb or webdav)")
			}
			secret, err := readBackupSecret(cmd.InOrStdin(), secretFile)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			if err := c.AddBackupTarget(ctx, &sdspb.AddBackupTargetRequest{
				Name: name, Kind: kind, Prefix: prefix,
				Bucket: bucket, Endpoint: endpoint, Region: region,
				Host: host, Share: share,
				User: user, Secret: secret, SecretObscured: secretObscured,
			}); err != nil {
				return fmt.Errorf("failed to add backup target: %w", err)
			}
			// Writes to the command's own output stream are best-effort. The only ways
			// they fail are a closed pipe (`sds ... | head`) or a full disk, neither of
			// which this command can report anywhere the operator is still looking, and
			// treating them as errors would report a successful operation as failed.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backup target %q saved\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Target name (required)")
	cmd.Flags().StringVar(&kind, "kind", "", "s3, smb or webdav (required)")
	cmd.Flags().StringVar(&prefix, "prefix", "", "Path prefix prepended to every object")
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "S3 endpoint URL, or the WebDAV collection URL")
	cmd.Flags().StringVar(&region, "region", "", "S3 region (default: us-east-1)")
	cmd.Flags().StringVar(&host, "host", "", "SMB server (host or host:port)")
	cmd.Flags().StringVar(&share, "share", "", "SMB share name")
	cmd.Flags().StringVar(&user, "user", "", "S3 access key id, or SMB/WebDAV username")
	cmd.Flags().StringVar(&secretFile, "secret-file", "",
		"Read the secret from this file (\"-\" for stdin); default: the "+secretEnvVar+" environment variable")
	cmd.Flags().BoolVar(&secretObscured, "secret-obscured", false,
		"The supplied secret is already in rclone's obscured form")
	return cmd
}

func backupTargetListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup targets (secrets are never shown)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			targets, err := c.ListBackupTargets(ctx)
			if err != nil {
				return fmt.Errorf("failed to list backup targets: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(targets) == 0 {
				_, _ = fmt.Fprintln(out, "No backup targets configured")
				return nil
			}
			for _, t := range targets {
				_, _ = fmt.Fprintf(out, "%s\n  %s\n", t.Name, t.Description)
				if t.User != "" {
					_, _ = fmt.Fprintf(out, "  user: %s\n", t.User)
				}
			}
			return nil
		},
	}
}

func backupTargetDeleteCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a backup target definition",
		Long: `Remove a backup target.

Refused while backups still reference it: losing the definition means losing the
ability to restore those backups or even to delete their objects. --force drops
it anyway, for a target whose storage is already gone.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)

			if err := c.DeleteBackupTarget(ctx, args[0], force); err != nil {
				return fmt.Errorf("failed to delete backup target: %w", err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backup target %q deleted\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Delete even if backups still reference the target")
	return cmd
}

// readBackupSecret resolves the object-store secret from --secret-file or the
// environment. A trailing newline is stripped because `echo secret > file` and
// heredocs both add one, and a secret with a stray newline fails to
// authenticate in a way that looks like a wrong password.
func readBackupSecret(stdin io.Reader, secretFile string) (string, error) {
	switch {
	case secretFile == "-":
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read secret from stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	case secretFile != "":
		data, err := os.ReadFile(secretFile)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	default:
		secret := os.Getenv(secretEnvVar)
		if secret == "" {
			return "", fmt.Errorf(
				"no secret supplied: set %s, or pass --secret-file (\"-\" reads stdin). "+
					"There is no --secret-key flag on purpose: it would land in your shell history", secretEnvVar)
		}
		return secret, nil
	}
}

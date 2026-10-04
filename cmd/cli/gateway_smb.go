package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	v1 "github.com/haify-project/sds/api/proto/v1"
	"github.com/haify-project/sds/pkg/client"
	"github.com/spf13/cobra"
)

func gatewaySMB() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smb",
		Short: "SMB gateway management (workgroup: standalone Samba, local users)",
	}
	cmd.AddCommand(smbCreate(), smbShareCommand(), smbUserCommand())
	return cmd
}

func smbCreate() *cobra.Command {
	var resource, serviceIP, workgroup, share string
	var readOnly bool
	var validUsers []string
	cmd := &cobra.Command{
		Use:   "create --resource <name> --service-ip <ip/cidr>",
		Short: "Create an SMB gateway sharing the resource's volume",
		Long: `Create an SMB gateway: a standalone Samba server on the service IP, failed
over with the resource. Its users and shares live on the gateway's state
volume. The first share covers the whole data volume; add users with
'sds gateway smb user set' before connecting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if resource == "" || serviceIP == "" {
				return fmt.Errorf("--resource and --service-ip are required")
			}
			c, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer closeClient(c)
			resp, err := c.CreateSMBGateway(cmd.Context(), &v1.CreateSMBGatewayRequest{
				Resource: resource, ServiceIp: serviceIP, Workgroup: workgroup,
				ShareName: share, ReadOnly: readOnly, ValidUsers: validUsers,
			})
			if err != nil {
				return fmt.Errorf("failed to create SMB gateway: %w", err)
			}
			if !resp.Success {
				return fmt.Errorf("failed to create SMB gateway: %s", resp.Message)
			}
			fmt.Printf("✓ %s\n  Config Path: %s\n", resp.Message, resp.ConfigPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource to share")
	cmd.Flags().StringVar(&serviceIP, "service-ip", "", "Service IP in CIDR notation, e.g. 192.168.1.210/24")
	cmd.Flags().StringVar(&workgroup, "workgroup", "", "Workgroup (default WORKGROUP)")
	cmd.Flags().StringVar(&share, "share", "", "Name of the first share (default: the resource name)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "Make the first share read-only")
	cmd.Flags().StringSliceVar(&validUsers, "valid-users", nil, "Users allowed on the first share (default: every user of the gateway)")
	return cmd
}

func smbShareCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "share", Short: "Shares of a running SMB gateway"}
	var resource string
	cmd.PersistentFlags().StringVar(&resource, "resource", "", "Gateway resource")

	var path string
	var readOnly bool
	var validUsers []string
	add := &cobra.Command{
		Use:   "add <name> --resource <name> [--path <dir>]",
		Short: "Add a share (a directory under the gateway's volume)",
		Args:  cobra.ExactArgs(1),
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, args []string) error {
			return c.AddSMBShare(cmd.Context(), resource, &v1.SMBShareInfo{
				Name: args[0], Path: path, ReadOnly: readOnly, ValidUsers: validUsers,
			})
		}, "share added"),
	}
	add.Flags().StringVar(&path, "path", "", "Directory under the volume's root (created); default the whole volume")
	add.Flags().BoolVar(&readOnly, "read-only", false, "Read-only share")
	add.Flags().StringSliceVar(&validUsers, "valid-users", nil, "Users allowed (default: every user of the gateway)")

	remove := &cobra.Command{
		Use:   "remove <name> --resource <name>",
		Short: "Remove a share (its data stays)",
		Args:  cobra.ExactArgs(1),
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, args []string) error {
			return c.RemoveSMBShare(cmd.Context(), resource, args[0])
		}, "share removed; its data was left in place"),
	}
	list := &cobra.Command{
		Use:   "list --resource <name>",
		Short: "List shares",
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, _ []string) error {
			shares, err := c.ListSMBShares(cmd.Context(), resource)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tPATH\tREAD-ONLY\tUSERS")
			for _, s := range shares {
				p, users := "/"+s.Path, strings.Join(s.ValidUsers, ",")
				if users == "" {
					users = "(all)"
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%v\t%s\n", s.Name, p, s.ReadOnly, users)
			}
			return w.Flush()
		}, ""),
	}
	cmd.AddCommand(add, remove, list)
	return cmd
}

func smbUserCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Users of a running SMB gateway"}
	var resource string
	var passwordStdin bool
	cmd.PersistentFlags().StringVar(&resource, "resource", "", "Gateway resource")

	set := &cobra.Command{
		Use:   "set <user> --resource <name> [--password-stdin]",
		Short: "Add a user, or change its password",
		Long: `Add an SMB user, or change its password. The password is read from
standard input: typed at the prompt, or piped with --password-stdin.`,
		Args: cobra.ExactArgs(1),
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, args []string) error {
			if !passwordStdin {
				fmt.Fprintf(os.Stderr, "Password for %s (echoed; use --password-stdin to pipe it): ", args[0])
			}
			pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
			if err != nil && pw == "" {
				return fmt.Errorf("read password: %w", err)
			}
			pw = strings.TrimRight(pw, "\r\n")
			return c.SetSMBUser(cmd.Context(), resource, args[0], pw)
		}, "user set"),
	}
	set.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read the password from standard input without prompting")
	remove := &cobra.Command{
		Use:   "remove <user> --resource <name>",
		Short: "Remove a user",
		Args:  cobra.ExactArgs(1),
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, args []string) error {
			return c.RemoveSMBUser(cmd.Context(), resource, args[0])
		}, "user removed"),
	}
	list := &cobra.Command{
		Use:   "list --resource <name>",
		Short: "List users",
		RunE: smbRun(&resource, func(cmd *cobra.Command, c smbClient, _ []string) error {
			users, err := c.ListSMBUsers(cmd.Context(), resource)
			if err != nil {
				return err
			}
			for _, u := range users {
				fmt.Println(u)
			}
			return nil
		}, ""),
	}
	cmd.AddCommand(set, remove, list)
	return cmd
}

type smbClient = *client.SDSClient

// smbRun wraps an SMB subcommand: requires --resource, connects, runs fn and
// prints done on success.
func smbRun(resource *string, fn func(*cobra.Command, smbClient, []string) error, done string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if *resource == "" {
			return fmt.Errorf("--resource is required")
		}
		c, err := newSDSClient()
		if err != nil {
			return fmt.Errorf("failed to connect to controller: %w", err)
		}
		defer closeClient(c)
		if err := fn(cmd, c, args); err != nil {
			return err
		}
		if done != "" {
			fmt.Printf("✓ %s\n", done)
		}
		return nil
	}
}

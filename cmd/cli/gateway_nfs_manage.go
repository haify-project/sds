package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/liliang-cn/sds/pkg/gateway"
	"github.com/spf13/cobra"
)

func gatewayServiceHost(serviceIP string) string {
	serviceIP = strings.TrimSpace(serviceIP)
	if serviceIP == "" {
		return ""
	}

	ip, _, err := net.ParseCIDR(serviceIP)
	if err == nil {
		return ip.String()
	}

	host, _, found := strings.Cut(serviceIP, "/")
	if found {
		return host
	}
	return serviceIP
}

func gatewayExportDirectory(resource, exportPath string) string {
	exportPath = strings.TrimSpace(exportPath)
	if exportPath == "" {
		return filepath.Join(gateway.DefaultExportBasePath, resource)
	}
	if strings.HasPrefix(exportPath, gateway.DefaultExportBasePath+string(filepath.Separator)) {
		return exportPath
	}
	return filepath.Join(gateway.DefaultExportBasePath, resource, strings.TrimPrefix(exportPath, "/"))
}

func nfsExportCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Manage NFS exports",
	}

	cmd.AddCommand(nfsExportAdd())
	cmd.AddCommand(nfsExportRemove())
	cmd.AddCommand(nfsExportList())

	return cmd
}

func nfsExportAdd() *cobra.Command {
	var resource, exportPath, clientSpec, exportOptions string
	var fsid int32

	cmd := &cobra.Command{
		Use:   "add --resource <name> --path <path>",
		Short: "Add an export to an NFS gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.AddNFSExport(cmd.Context(), resource, exportPath, fsid, clientSpec, exportOptions)
			if err != nil {
				return fmt.Errorf("failed to add NFS export: %w", err)
			}

			fmt.Printf("NFS export added to '%s': %s\n", resource, gatewayExportDirectory(resource, exportPath))
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&exportPath, "path", "", "Export path")
	cmd.Flags().Int32Var(&fsid, "fsid", 0, "Filesystem ID (optional)")
	cmd.Flags().StringVar(&clientSpec, "client", "", "Client spec (e.g. 10.0.0.0/8)")
	cmd.Flags().StringVar(&exportOptions, "options", "", "Export options")
	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("path")

	return cmd
}

func nfsExportRemove() *cobra.Command {
	var resource, exportPath string

	cmd := &cobra.Command{
		Use:   "remove --resource <name> --path <path>",
		Short: "Remove an export from an NFS gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			err = sdsClient.RemoveNFSExport(cmd.Context(), resource, exportPath)
			if err != nil {
				return fmt.Errorf("failed to remove NFS export: %w", err)
			}

			fmt.Printf("NFS export removed from '%s': %s\n", resource, gatewayExportDirectory(resource, exportPath))
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&exportPath, "path", "", "Export path")
	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("path")

	return cmd
}

func nfsExportList() *cobra.Command {
	var resource string

	cmd := &cobra.Command{
		Use:   "list --resource <name>",
		Short: "List exports on an NFS gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			exports, err := sdsClient.ListNFSExports(cmd.Context(), resource)
			if err != nil {
				return fmt.Errorf("failed to list NFS exports: %w", err)
			}

			if len(exports) == 0 {
				fmt.Printf("No exports configured on '%s'\n", resource)
				return nil
			}

			fmt.Printf("NFS exports for '%s':\n", resource)
			for _, export := range exports {
				fmt.Printf("  - %s fsid=%s client=%s options=%s\n",
					export.Directory,
					export.Fsid,
					export.Clientspec,
					export.Options,
				)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	_ = cmd.MarkFlagRequired("resource")

	return cmd
}

func nfsMount() *cobra.Command {
	var resource, target, server, exportPath, mountOptions string
	var useSudo, printOnly, mkdir bool

	cmd := &cobra.Command{
		Use:   "mount --resource <name> --target <path>",
		Short: "Mount an NFS gateway on the local machine",
		Long:  "Mount the selected NFS gateway on the local machine where this CLI command is executed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			sdsClient, err := newSDSClient()
			if err != nil {
				return fmt.Errorf("failed to connect to controller: %w", err)
			}
			defer sdsClient.Close()

			gw, err := sdsClient.GetGateway(cmd.Context(), resource)
			if err != nil {
				return fmt.Errorf("failed to get gateway: %w", err)
			}
			if gw.Type != "nfs" {
				return fmt.Errorf("gateway '%s' is not an NFS gateway", resource)
			}

			if server == "" {
				server = gw.Options["service_host"]
				if server == "" {
					server = gatewayServiceHost(gw.Options["service_ip"])
				}
			}
			if exportPath == "" {
				exportPath = gw.Path
				if exportPath == "" {
					exportPath = gw.Options["export_directory"]
				}
				if exportPath == "" && gw.Options["export_path"] != "" {
					exportPath = gatewayExportDirectory(resource, gw.Options["export_path"])
				}
			}
			if server == "" {
				return fmt.Errorf("failed to determine NFS server address")
			}
			if exportPath == "" {
				return fmt.Errorf("failed to determine NFS export path")
			}

			if mkdir {
				if err := os.MkdirAll(target, 0755); err != nil {
					return fmt.Errorf("failed to create mount target: %w", err)
				}
			}

			mountArgs := buildNFSMountArgs(server, exportPath, target, mountOptions)
			displayArgs := append([]string(nil), mountArgs...)
			if useSudo {
				displayArgs = append([]string{"sudo"}, displayArgs...)
			}

			fmt.Printf("Mount command: %s\n", strings.Join(displayArgs, " "))
			if printOnly {
				return nil
			}

			commandName := mountArgs[0]
			commandArgs := mountArgs[1:]
			if useSudo {
				commandName = "sudo"
				commandArgs = append([]string{mountArgs[0]}, mountArgs[1:]...)
			}

			command := exec.CommandContext(cmd.Context(), commandName, commandArgs...)
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			command.Stdin = os.Stdin

			if err := command.Run(); err != nil {
				return fmt.Errorf("failed to mount NFS gateway: %w", err)
			}

			fmt.Printf("Mounted %s:%s on %s\n", server, exportPath, target)
			return nil
		},
	}

	cmd.Flags().StringVar(&resource, "resource", "", "DRBD resource name")
	cmd.Flags().StringVar(&target, "target", "", "Local mount target")
	cmd.Flags().StringVar(&server, "server", "", "Override NFS server address")
	cmd.Flags().StringVar(&exportPath, "export-path", "", "Override exported directory")
	cmd.Flags().StringVar(&mountOptions, "options", "", "Mount options passed to mount -o")
	cmd.Flags().BoolVar(&useSudo, "sudo", false, "Run mount through sudo")
	cmd.Flags().BoolVar(&printOnly, "print-only", false, "Only print the mount command")
	cmd.Flags().BoolVar(&mkdir, "mkdir", false, "Create the mount target before mounting")
	_ = cmd.MarkFlagRequired("resource")
	_ = cmd.MarkFlagRequired("target")

	return cmd
}

func buildNFSMountArgs(server, exportPath, target, mountOptions string) []string {
	args := []string{"mount", "-t", "nfs"}
	if strings.TrimSpace(mountOptions) != "" {
		args = append(args, "-o", mountOptions)
	}
	args = append(args, fmt.Sprintf("%s:%s", server, exportPath), target)
	return args
}

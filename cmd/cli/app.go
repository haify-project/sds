package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/haify-project/haify/pkg/client"
)

// `haify app`: a single-instance PostgreSQL, MySQL/MariaDB, Redis or RustFS on a
// resource's DRBD volume, failed over by drbd-reactor like a gateway.

func appCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Highly available databases and object stores on a resource (postgres, mysql, redis, rustfs)",
		Long: `Run one database instance on a resource's DRBD volume. drbd-reactor mounts the
volume, starts the database and raises the service IP on the node where the
resource is Primary, and does the same on another replica when that node fails,
with every write DRBD acknowledged (protocol C). Clients connect to the service IP.

The resource must exist and have at least two diskful replicas; the engine must
be installed, in the same version and with the same daemon uid/gid, on every one.`,
	}
	cmd.AddCommand(appCreate(), appList(), appStatus(), appDelete(), appFailover(), appSnapshot())
	return cmd
}

// appName takes the app's name from the argument or --name.
func appName(args []string, flag string) (string, error) {
	switch {
	case len(args) > 0 && flag != "" && args[0] != flag:
		return "", fmt.Errorf("the name is given twice (%q and --name %q)", args[0], flag)
	case len(args) > 0:
		return args[0], nil
	case flag != "":
		return flag, nil
	}
	return "", fmt.Errorf("the app's name is required")
}

// withApps runs fn with a connected client.
func withApps(timeout time.Duration, fn func(ctx context.Context, c *client.HaifyClient) error) error {
	c, err := newHaifyClient()
	if err != nil {
		return fmt.Errorf("failed to connect to controller: %w", err)
	}
	defer closeClient(c)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return fn(ctx, c)
}

func appCreate() *cobra.Command {
	var req client.AppCreateRequest
	var port int
	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an app on an existing resource",
		Example: `  haify app create --name orders --engine postgres --resource orders --service-ip 192.168.1.60/24
  haify app create --name vectors --engine postgres --vector --service-ip 192.168.1.61/24
  haify app create --name cache --engine redis --port 6380 --service-ip 192.168.1.62/24`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args, req.Name)
			if err != nil {
				return err
			}
			req.Name = name
			if req.Engine == "" || req.ServiceIP == "" {
				return fmt.Errorf("--engine and --service-ip are required")
			}
			if port < 0 || port > 65535 {
				return fmt.Errorf("--port %d is outside 0-65535", port)
			}
			req.Port = uint32(port)
			return withApps(15*time.Minute, func(ctx context.Context, c *client.HaifyClient) error {
				resp, err := c.CreateApp(ctx, req)
				if err != nil {
					return err
				}
				fmt.Println(resp.Message)
				a := resp.App
				fmt.Printf("  Engine:      %s %s\n", a.GetEngine(), a.GetVersion())
				fmt.Printf("  Resource:    %s (initialized on %s)\n", a.GetResource(), resp.PrimaryNode)
				fmt.Printf("  Service IP:  %s port %d\n", a.GetServiceIp(), a.GetPort())
				fmt.Printf("  Connect:     %s\n", a.GetConnection())
				if resp.Password != "" && a.GetEngine() == "rustfs" {
					fmt.Printf("  Access key:  %s\n", a.GetAdminUser())
					fmt.Printf("  Secret key:  %s\n", resp.Password)
				} else if resp.Password != "" {
					fmt.Printf("  User:        %s\n", a.GetAdminUser())
					fmt.Printf("  Password:    %s\n", resp.Password)
				}
				if resp.Password != "" {
					fmt.Printf("\nShown once. It is also kept, root-only, in %s on the\n"+
						"node running the app.\n", a.GetCredentialsFile())
				}
				fmt.Printf("\nFollow it with: haify app status %s\n", a.GetName())
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&req.Name, "name", "", "App name (lower case, digits, hyphens)")
	cmd.Flags().StringVar(&req.Engine, "engine", "", "Engine: postgres, mysql, redis or rustfs (S3; its console takes the port after --port)")
	cmd.Flags().StringVar(&req.Resource, "resource", "", "Resource to run it on (default: the app's name)")
	cmd.Flags().StringVar(&req.ServiceIP, "service-ip", "", "Service IP clients connect to, in CIDR notation")
	cmd.Flags().IntVar(&port, "port", 0, "TCP port (default: 5432, 3306 or 6379)")
	cmd.Flags().BoolVar(&req.Vector, "vector", false, "postgres: install the pgvector extension")
	return cmd
}

func appList() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApps(30*time.Second, func(ctx context.Context, c *client.HaifyClient) error {
				apps, err := c.ListApps(ctx)
				if err != nil {
					return err
				}
				if len(apps) == 0 {
					fmt.Println("No apps")
					return nil
				}
				rows := make([][]string, 0, len(apps))
				for _, a := range apps {
					engine := a.GetEngine()
					if a.GetVector() {
						engine += "+vector"
					}
					rows = append(rows, []string{a.GetName(), engine, a.GetResource(), a.GetServiceIp(),
						strconv.Itoa(int(a.GetPort()))})
				}
				fmt.Print(RenderStaticTable(fmt.Sprintf("Apps (%d)", len(apps)),
					[]string{"NAME", "ENGINE", "RESOURCE", "SERVICE IP", "PORT"}, rows, nil))
				return nil
			})
		},
	}
}

func appStatus() *cobra.Command {
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "status [name]",
		Short: "Show where an app runs and whether it answers",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args, nameFlag)
			if err != nil {
				return err
			}
			return withApps(60*time.Second, func(ctx context.Context, c *client.HaifyClient) error {
				st, err := c.GetAppStatus(ctx, name)
				if err != nil {
					return err
				}
				a := st.App
				primary := st.PrimaryNode
				if primary == "" {
					primary = "(none)"
				}
				health := "no answer"
				if st.Healthy {
					health = "answers"
				}
				fmt.Printf("App %s: %s\n", a.GetName(), st.State)
				fmt.Printf("  Engine:      %s %s\n", a.GetEngine(), a.GetVersion())
				fmt.Printf("  Resource:    %s\n", a.GetResource())
				fmt.Printf("  Running on:  %s\n", primary)
				if st.PrimaryNode != "" {
					fmt.Printf("  Unit:        %s (%s)\n", a.GetUnit(), st.ServiceState)
					fmt.Printf("  Database:    %s\n", health)
				}
				fmt.Printf("  Nodes:       %s\n", strings.Join(st.Nodes, ", "))
				fmt.Printf("  Service IP:  %s port %d\n", a.GetServiceIp(), a.GetPort())
				fmt.Printf("  Connect:     %s\n", a.GetConnection())
				fmt.Printf("  Credentials: %s (root, on the node running it)\n", a.GetCredentialsFile())
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&nameFlag, "name", "", "App name")
	return cmd
}

func appDelete() *cobra.Command {
	var nameFlag string
	var deleteData, yes bool
	cmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Stop an app and take it out of drbd-reactor (the data is kept)",
		Long: `Disables the app's promoter, stops the database and removes its unit and config
from every node. The resource and the data on it are kept, and creating the app
again on it picks the data up. --delete-data also deletes the resource.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args, nameFlag)
			if err != nil {
				return err
			}
			if deleteData && !yes {
				fmt.Printf("This deletes app %q and its resource with every byte of its data. Re-run with --yes to proceed.\n", name)
				return nil
			}
			return withApps(10*time.Minute, func(ctx context.Context, c *client.HaifyClient) error {
				msg, err := c.DeleteApp(ctx, name, deleteData)
				if err != nil {
					return err
				}
				fmt.Println(msg)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&nameFlag, "name", "", "App name")
	cmd.Flags().BoolVar(&deleteData, "delete-data", false, "Also delete the resource and its data")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm --delete-data")
	return cmd
}

func appFailover() *cobra.Command {
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "failover [name]",
		Short: "Move an app to another replica (planned switchover)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args, nameFlag)
			if err != nil {
				return err
			}
			return withApps(5*time.Minute, func(ctx context.Context, c *client.HaifyClient) error {
				resp, err := c.FailoverApp(ctx, name)
				if err != nil {
					return err
				}
				fmt.Println(resp.Message)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&nameFlag, "name", "", "App name")
	return cmd
}

func appSnapshot() *cobra.Command {
	var nameFlag, snapshot string
	cmd := &cobra.Command{
		Use:   "snapshot [name] --snapshot <snap>",
		Short: "Snapshot an app on every replica with the database frozen",
		Long: `Flushes and freezes the database on its node (postgres: CHECKPOINT; mysql: FLUSH
TABLES WITH READ LOCK; redis: BGSAVE; then fsfreeze), takes a resource snapshot
of every volume on every replica, and thaws. The node thaws the database by
itself after 60 seconds if the controller does not. Roll back with
haify resource snapshot replicated rollback once the app is deleted or stopped.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := appName(args, nameFlag)
			if err != nil {
				return err
			}
			if snapshot == "" {
				return fmt.Errorf("--snapshot is required")
			}
			return withApps(10*time.Minute, func(ctx context.Context, c *client.HaifyClient) error {
				resp, err := c.SnapshotApp(ctx, name, snapshot)
				if err != nil {
					return err
				}
				fmt.Println(resp.Message)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&nameFlag, "name", "", "App name")
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "Snapshot name")
	return cmd
}

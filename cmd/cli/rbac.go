package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/spf13/cobra"
)

// The RBAC introspection endpoints are plain JSON on the REST gateway (port
// 3375), not gRPC, so these commands speak HTTP directly. The REST host is the
// controller host with the fixed REST port.
const restPort = "3375"

func rbacRestURL(path string) string {
	host := controllerAddr
	if h, _, err := net.SplitHostPort(controllerAddr); err == nil {
		host = h
	}
	return fmt.Sprintf("http://%s/%s", net.JoinHostPort(host, restPort), path)
}

func rbacGet(path string, out any) (int, error) {
	return rbacRequest(http.MethodGet, path, nil, out)
}

func rbacRequest(method, path string, payload any, out any) (int, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rbacRestURL(path), bodyReader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := client.ResolveToken(tokenFlag); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to reach controller REST API: %w", err)
	}
	// Closing a response body only releases the connection back to the pool;
	// the read that mattered is the ReadAll below, and its error is what the
	// caller needs. A close failure here cannot invalidate a body already read.
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if len(body) > 0 && out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, fmt.Errorf("invalid response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func rbacCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rbac",
		Short: "Inspect role-based access control (identity and policy)",
	}
	cmd.AddCommand(rbacWhoamiCommand())
	cmd.AddCommand(rbacPoliciesCommand())
	cmd.AddCommand(rbacUserCommand())
	return cmd
}

func rbacUserCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage RBAC users (admin only)",
	}
	cmd.AddCommand(rbacUserAddCommand())
	cmd.AddCommand(rbacUserRemoveCommand())
	cmd.AddCommand(rbacUserSetRoleCommand())
	return cmd
}

func rbacUserAddCommand() *cobra.Command {
	var name, role, token string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a user with a role (a token is generated unless --token is given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || role == "" {
				return fmt.Errorf("--name and --role are required")
			}
			var res struct {
				Token string `json:"token"`
				Error string `json:"error"`
			}
			code, err := rbacRequest(http.MethodPost, "v1/rbac/users",
				map[string]string{"name": name, "role": role, "token": token}, &res)
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", orDefault(res.Error, "request failed"))
			}
			fmt.Printf("User %q created with role %q.\n", name, role)
			fmt.Printf("Token (store it now, it is not shown again):\n  %s\n", res.Token)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "User name")
	cmd.Flags().StringVar(&role, "role", "", "Role (admin, operator, viewer)")
	cmd.Flags().StringVar(&token, "token", "", "Explicit token (optional; min 16 chars)")
	return cmd
}

func rbacUserRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Error string `json:"error"`
			}
			code, err := rbacRequest(http.MethodDelete, "v1/rbac/users/"+args[0], nil, &res)
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", orDefault(res.Error, "request failed"))
			}
			fmt.Printf("User %q removed.\n", args[0])
			return nil
		},
	}
}

func rbacUserSetRoleCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "set-role <name> <role>",
		Short: "Change a user's role",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Error string `json:"error"`
			}
			code, err := rbacRequest(http.MethodPut, "v1/rbac/users/"+args[0]+"/role",
				map[string]string{"role": args[1]}, &res)
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", orDefault(res.Error, "request failed"))
			}
			fmt.Printf("User %q is now %q.\n", args[0], args[1])
			return nil
		},
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func rbacWhoamiCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the identity and role of your API token",
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Enabled  bool   `json:"enabled"`
				User     string `json:"user"`
				Role     string `json:"role"`
				CanAdmin bool   `json:"can_admin"`
				Error    string `json:"error"`
			}
			code, err := rbacGet("v1/rbac/whoami", &res)
			if err != nil {
				return err
			}
			if !res.Enabled {
				fmt.Println("RBAC is not enabled on the controller (single-token auth).")
				return nil
			}
			if code != http.StatusOK || res.User == "" {
				msg := res.Error
				if msg == "" {
					msg = "unauthorized"
				}
				return fmt.Errorf("%s", msg)
			}
			fmt.Printf("User:      %s\n", res.User)
			fmt.Printf("Role:      %s\n", res.Role)
			fmt.Printf("Admin:     %v\n", res.CanAdmin)
			return nil
		},
	}
}

func rbacPoliciesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "policies",
		Short: "Show effective roles and user assignments (admin only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var res struct {
				Enabled  bool `json:"enabled"`
				Policies []struct {
					Role   string `json:"role"`
					Object string `json:"object"`
					Action string `json:"action"`
				} `json:"policies"`
				Users []struct {
					Name string `json:"name"`
					Role string `json:"role"`
				} `json:"users"`
				Error string `json:"error"`
			}
			code, err := rbacGet("v1/rbac/policies", &res)
			if err != nil {
				return err
			}
			if !res.Enabled {
				fmt.Println("RBAC is not enabled on the controller (single-token auth).")
				return nil
			}
			if code != http.StatusOK {
				msg := res.Error
				if msg == "" {
					msg = "request failed"
				}
				return fmt.Errorf("%s", msg)
			}

			fmt.Println("Users")
			uw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			// Writes to the command's own output stream are best-effort. The only ways
			// they fail are a closed pipe (`sds ... | head`) or a full disk, neither of
			// which this command can report anywhere the operator is still looking, and
			// treating them as errors would report a successful operation as failed.
			_, _ = fmt.Fprintln(uw, "  NAME\tROLE")
			for _, u := range res.Users {
				_, _ = fmt.Fprintf(uw, "  %s\t%s\n", u.Name, u.Role)
			}
			// Flush pushes the buffered table to stdout; like the Fprint calls above it
			// is best-effort, and a write failure here says nothing about whether the
			// operation the operator asked for succeeded.
			_ = uw.Flush()

			fmt.Println("\nPolicies")
			pw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			_, _ = fmt.Fprintln(pw, "  ROLE\tOBJECT\tACTION")
			for _, p := range res.Policies {
				_, _ = fmt.Fprintf(pw, "  %s\t%s\t%s\n", p.Role, p.Object, p.Action)
			}
			_ = pw.Flush()
			return nil
		},
	}
}

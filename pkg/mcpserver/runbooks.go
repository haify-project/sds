package mcpserver

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools say what can be done; they cannot say in what order, or which
// call fails halfway if done first. Those live in runbooks, served two ways
// from the same text: as MCP prompts, which Claude Code offers as slash
// commands, and through sds_runbook, a read-only tool, because ChatGPT and
// claude.ai connectors take tools and ignore prompts, and the AI Copilot
// reads through tools alone.

//go:embed runbooks/*.md
var runbookFS embed.FS

type runbook struct {
	Name    string
	Title   string
	Summary string
	Needs   string
	Body    string
}

// loadRunbooks parses runbooks/*.md: a "# Title" line, a one-line summary, a
// "Needs: <role>" line, then the steps.
func loadRunbooks() ([]runbook, error) {
	entries, err := runbookFS.ReadDir("runbooks")
	if err != nil {
		return nil, err
	}
	var out []runbook
	for _, e := range entries {
		raw, err := runbookFS.ReadFile("runbooks/" + e.Name())
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) < 4 || !strings.HasPrefix(lines[0], "# ") || !strings.HasPrefix(lines[2], "Needs: ") {
			return nil, fmt.Errorf("runbooks/%s: want '# Title', a summary line, 'Needs: <role>', then the steps", e.Name())
		}
		out = append(out, runbook{
			Name:    strings.TrimSuffix(e.Name(), ".md"),
			Title:   strings.TrimPrefix(lines[0], "# "),
			Summary: strings.TrimSpace(lines[1]),
			Needs:   strings.TrimPrefix(lines[2], "Needs: "),
			Body:    strings.TrimSpace(strings.Join(lines, "\n")),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type runbookIn struct {
	Name string `json:"name,omitempty" jsonschema:"runbook to read; empty lists them all"`
}

type runbookOut struct {
	Runbooks []runbookInfo `json:"runbooks,omitempty"`
	Runbook  string        `json:"runbook,omitempty"`
}

type runbookInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Needs   string `json:"needs" jsonschema:"the token role the steps need: operate or admin"`
}

func (s *Server) registerRunbooks(srv *mcp.Server) {
	books, err := loadRunbooks()
	if err != nil {
		// Embedded at build time and covered by a test: not reachable in a
		// released binary, and a server without runbooks is still a server.
		s.logger.Error("runbooks not loaded")
		return
	}
	byName := make(map[string]runbook, len(books))
	infos := make([]runbookInfo, 0, len(books))
	for _, b := range books {
		byName[b.Name] = b
		infos = append(infos, runbookInfo{Name: b.Name, Title: b.Title, Summary: b.Summary, Needs: b.Needs})

		srv.AddPrompt(&mcp.Prompt{
			Name:        b.Name,
			Title:       b.Title,
			Description: b.Summary + " (needs " + b.Needs + ")",
			Arguments: []*mcp.PromptArgument{{
				Name: "target", Description: "the resource or node this is for", Required: false,
			}},
		}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			text := b.Body
			if t := strings.TrimSpace(req.Params.Arguments["target"]); t != "" {
				text += "\n\nApplies to: " + t
			}
			return &mcp.GetPromptResult{
				Description: b.Summary,
				Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}},
			}, nil
		})
	}

	addRead(s, srv, readOnlyTool("sds_runbook", "Read an operations runbook",
		"The order to do a multi-step operation in, and what fails halfway if it is done in another: switching the "+
			"controller or a gateway to another node, taking a node out of service, adding a replica, growing a "+
			"volume, checking and repairing replica data, failing a WAN resource back, renumbering nodes. Read the "+
			"runbook before starting such an operation. With no name it lists them."),
		func(_ context.Context, _ *mcp.CallToolRequest, in runbookIn) (*mcp.CallToolResult, runbookOut, error) {
			name := strings.TrimSpace(in.Name)
			if name == "" {
				return nil, runbookOut{Runbooks: infos}, nil
			}
			b, ok := byName[name]
			if !ok {
				names := make([]string, 0, len(byName))
				for n := range byName {
					names = append(names, n)
				}
				sort.Strings(names)
				return nil, runbookOut{}, fmt.Errorf("no runbook %q; there are %s", name, strings.Join(names, ", "))
			}
			return nil, runbookOut{Runbook: b.Body}, nil
		})
}

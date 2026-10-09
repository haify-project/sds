package alert

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/haify-project/haify/pkg/event"
)

func (m *Monitor) checkNodes(ctx context.Context, sc *pollScope, obs *Observation) {
	if m.nodes == nil {
		return
	}
	obs.Nodes.Enabled = true

	start := time.Now()
	nodes, err := m.nodes.GetNodeStatusList(ctx)
	obs.Nodes.Duration = time.Since(start)
	if err != nil {
		obs.Nodes.Err = err
		m.log.Warn("alert monitor: list nodes failed", zap.Error(err))
		return
	}
	obs.Nodes.OK = true
	obs.Nodes.Items = nodes
	sc.nodesOK = true

	for _, n := range nodes {
		msg := n.Message
		if msg == "" {
			msg = "no response"
		}
		m.level(event.Event{
			Type:     event.TypeNodeUnreachable,
			Severity: event.SeverityCritical,
			Node:     n.Name,
		}, sc, sourceNodes, !n.Reachable,
			fmt.Sprintf("node %s is unreachable: %s", n.Name, msg),
			fmt.Sprintf("node %s is reachable again", n.Name))
	}
}

/**
 * Marks the one node currently running the control plane.
 *
 * This replaced a per-node "role in <resource>" column on the Dashboard, which
 * asked a question most rows had no answer to. Self-HA membership is a property
 * of two nodes; a node list shows every node in the cluster, so the column read
 * "not a member" both for a full third replica carrying a quorum vote and for a
 * DR node with no copy of that resource at all — one grey label for two facts
 * that could not be more different, and the first of them is the reason the
 * cluster survives losing a member.
 *
 * Membership belongs to the Self-HA card, which already names it. What a node
 * list needs is the one thing about a node that moves on its own: whether the
 * controller is here right now. After a failover that is the only row in the
 * table whose meaning changed.
 */
export function ControllerChip() {
  return (
    <span
      data-slot="controller-chip"
      className="inline-flex items-center rounded-[5px] bg-accent px-1.5 py-0.5 text-[10.5px] font-medium text-accent-foreground"
      title="This node currently runs the controller and holds the VIP"
    >
      controller
    </span>
  );
}

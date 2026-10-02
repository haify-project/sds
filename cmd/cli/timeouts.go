package main

import "time"

// nodeOpTimeout bounds a command that changes state on storage nodes: LVM,
// DRBD and reactor work over SSH on every replica. A thirty-second limit made
// the CLI report a timeout while the controller went on to finish (an
// add-volume on a slow node took 29.7 s), which reads as a failure that
// wasn't one. Listings and status keep the short timeout.
const nodeOpTimeout = 10 * time.Minute

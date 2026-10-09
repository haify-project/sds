// dbmigrate copies an SDS controller database into a Haify one, renaming
// what the new controller derives from its own name. History (audit, events,
// inspections, deliveries) is copied unchanged; backups keep their resource
// names and object paths, because those name real objects in the bucket.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	bolt "go.etcd.io/bbolt"
)

var history = map[string]bool{"audit": true, "audit_ship": true, "events": true, "inspections": true, "notify_delivery": true}

var valueRules = []string{
	"sds-meta", "haify-meta",
	"/dev/sds_", "/dev/haify_",
	`"sds_`, `"haify_`,
	`/sds_`, `/haify_`,
	"/var/lib/sds", "/var/lib/haify",
	"sds-controller.service", "haify-controller.service",
	"sds-ai.service", "haify-ai.service",
	"sds-mcp-http.service", "haify-mcp-http.service",
	`"sds.`, `"haify.`,
	`\"sds.`, `\"haify.`,
	"sdsthin", "haifythin",
}

var backupRules = []string{"/dev/sds_", "/dev/haify_", `"sds_`, `"haify_`, `/sds_`, `/haify_`, "sdsthin", "haifythin"}

func apply(rules []string, s string) string {
	for i := 0; i < len(rules); i += 2 {
		s = strings.ReplaceAll(s, rules[i], rules[i+1])
	}
	return s
}

func renameKey(k string) string {
	k = strings.ReplaceAll(k, "sds-meta", "haify-meta")
	if strings.HasPrefix(k, "sds_") {
		k = "haify_" + strings.TrimPrefix(k, "sds_")
	}
	return k
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: dbmigrate <sds.db> <haify.db>")
		os.Exit(2)
	}
	src, err := bolt.Open(os.Args[1], 0600, &bolt.Options{ReadOnly: true})
	must(err)
	defer func() { _ = src.Close() }()
	if _, err := os.Stat(os.Args[2]); err == nil {
		must(fmt.Errorf("%s exists", os.Args[2]))
	}
	dst, err := bolt.Open(os.Args[2], 0600, nil)
	must(err)
	// Closing flushes nothing that Update has not already committed, but a
	// failure here still means the file may not be complete.
	defer func() { must(dst.Close()) }()

	must(src.View(func(stx *bolt.Tx) error {
		return dst.Update(func(dtx *bolt.Tx) error {
			return stx.ForEach(func(name []byte, sb *bolt.Bucket) error {
				db, err := dtx.CreateBucket(name)
				if err != nil {
					return err
				}
				db.FillPercent = 1
				bucket := string(name)
				changed := 0
				err = sb.ForEach(func(k, v []byte) error {
					if v == nil {
						return fmt.Errorf("nested bucket %s/%s not handled", bucket, k)
					}
					nk, nv := string(k), string(v)
					switch {
					case history[bucket]:
					case bucket == "backups":
						nv = apply(backupRules, nv)
					default:
						nk = renameKey(nk)
						nv = apply(valueRules, nv)
					}
					if nk != string(k) || nv != string(v) {
						changed++
						fmt.Printf("[%s] %s%s\n", bucket, k, map[bool]string{true: " -> " + nk, false: ""}[nk != string(k)])
						if nv != string(v) {
							fmt.Printf("    %s\n", diff(string(v), nv))
						}
					}
					return db.Put([]byte(nk), []byte(nv))
				})
				if err == nil {
					seq := sb.Sequence()
					err = db.SetSequence(seq)
				}
				return err
			})
		})
	}))
	// Prove nothing derived is left outside history and backups.
	must(dst.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			if history[string(name)] || string(name) == "backups" {
				return nil
			}
			return b.ForEach(func(k, v []byte) error {
				for _, bad := range []string{"sds-meta", "/var/lib/sds", "/dev/sds_", `"sds_`, `"sds.`, `\"sds.`, "sds-controller"} {
					if bytes.Contains(k, []byte(bad)) || bytes.Contains(v, []byte(bad)) {
						fmt.Printf("LEFT [%s] %s contains %s\n", name, k, bad)
					}
				}
				return nil
			})
		})
	}))
}

// diff shows the changed span of a value.
func diff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	ja, jb := len(a), len(b)
	for ja > i && jb > i && a[ja-1] == b[jb-1] {
		ja--
		jb--
	}
	lo := max(0, i-30)
	return fmt.Sprintf("…%s[%s => %s]%s…", a[lo:i], a[i:ja], b[i:jb], a[ja:min(len(a), ja+30)])
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbmigrate:", err)
		os.Exit(1)
	}
}

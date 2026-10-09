// Package apptemplate generates what it takes to run a single-instance
// database on a Haify resource, failed over by drbd-reactor: the systemd unit,
// the promoter config, the one-time initialization script, the health probe,
// the freeze/thaw pair for consistent snapshots and the prerequisite probe.
//
// Everything here is pure text generation and parsing. The controller
// (pkg/controller/app*.go) decides where each piece runs; keeping the pieces
// here means every one of them can be read and tested without a cluster.
//
// The shape of an app follows from how drbd-reactor fails a resource over:
//
//   - Data, configuration and credentials all live on the DRBD volume,
//     mounted at /var/lib/haify-app/<name>, so they move together. A config file
//     left on one node's root filesystem is a config the next node lacks.
//   - The unit has no [Install] section and is never enabled: only the
//     promoter starts it, on the node that is Primary.
//   - The service IP is the last entry of the start list, so clients reach a
//     node only once the database answers there, and it is the first thing
//     taken away on the way out.
package apptemplate

import (
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// MountBase is where the Primary mounts each app's volume, one directory
	// per app. It is deliberately outside /var/lib/haify (the controller's own
	// Self-HA mount) and /var/lib/haify-gateway: promoters move independently,
	// and a mount covered by another cannot be unmounted by path.
	MountBase = "/var/lib/haify-app"
	// ReactorConfigDir holds drbd-reactor's promoter snippets.
	ReactorConfigDir = "/etc/drbd-reactor.d"
	// UnitDir is where the app's unit file is written on every node.
	UnitDir = "/etc/systemd/system"
	// RuntimeBase prefixes the per-app runtime directory (sockets, pid files),
	// which systemd creates and removes with the unit.
	RuntimeBase = "/run/haify-app-"
	// FreezeWatchdog is how long a node keeps an app frozen for a snapshot
	// before it thaws it on its own, whatever happened to the controller.
	FreezeWatchdog = 60 * time.Second
	// ReadyTimeout bounds how long the unit waits for the database to answer
	// after starting it (crash recovery after a failover included).
	ReadyTimeout = 300
)

// Engine is a database the app templates know how to run.
type Engine string

const (
	// Postgres is PostgreSQL, optionally with the pgvector extension.
	Postgres Engine = "postgres"
	// MySQL is MySQL or MariaDB, whichever the nodes have installed.
	MySQL Engine = "mysql"
	// Redis is Redis (or a drop-in compatible server providing redis-server).
	Redis Engine = "redis"
)

// ErrInvalid marks a request rejected before anything was done on a node.
var ErrInvalid = errors.New("invalid app request")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// engineInfo is what differs between engines apart from scripts.
type engineInfo struct {
	user        string // OS user the daemon runs as
	adminUser   string // database account whose password is generated
	defaultPort int
	packages    string // what to install, for error messages and docs
}

var engines = map[Engine]engineInfo{
	Postgres: {user: "postgres", adminUser: "postgres", defaultPort: 5432,
		packages: "postgresql (Debian/Ubuntu, EL) of the same major version on every node; " +
			"for --vector also postgresql-<major>-pgvector (Debian/Ubuntu) or pgvector_<major> (PGDG EL)"},
	MySQL: {user: "mysql", adminUser: "root", defaultPort: 3306,
		packages: "mariadb-server (Debian/Ubuntu, EL) or mysql-server, the same version on every node"},
	Redis: {user: "redis", adminUser: "default", defaultPort: 6379,
		packages: "redis-server (Debian/Ubuntu) or redis (EL), the same version on every node"},
}

// Engines lists the engine names an app can be created with.
func Engines() []string {
	out := make([]string, 0, len(engines))
	for e := range engines {
		out = append(out, string(e))
	}
	sort.Strings(out)
	return out
}

// Packages names what an engine needs installed on every diskful node.
func Packages(e Engine) string { return engines[e].packages }

// OSUser is the OS account the engine's daemon runs as.
func OSUser(e Engine) string { return engines[e].user }

// AdminUser is the database account whose password `app create` generates.
func AdminUser(e Engine) string { return engines[e].adminUser }

// DefaultPort is the engine's usual TCP port.
func DefaultPort(e Engine) int { return engines[e].defaultPort }

// Spec is one app as the caller asked for it.
type Spec struct {
	Name      string
	Engine    Engine
	Resource  string
	ServiceIP string // CIDR, e.g. 192.168.1.60/24
	Port      int    // 0 means the engine's default
	Vector    bool   // postgres only: install pgvector
}

// An app name becomes part of a systemd unit name, a directory and a
// drbd-reactor config name; lower case, digits and inner hyphens keep it valid
// in all three and safe to put in a shell script unquoted.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// resourceRe accepts the DRBD resource names the generated files can carry
// unquoted. The resource must exist anyway; this only keeps it shell-safe.
var resourceRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// Normalize validates s and fills its defaults: the resource defaults to the
// app's name and the port to the engine's own.
func (s *Spec) Normalize() error {
	s.Name = strings.TrimSpace(s.Name)
	s.Engine = Engine(strings.ToLower(strings.TrimSpace(string(s.Engine))))
	s.Resource = strings.TrimSpace(s.Resource)
	s.ServiceIP = strings.TrimSpace(s.ServiceIP)

	if !nameRe.MatchString(s.Name) {
		return invalid("name %q must be 1-40 lower-case letters, digits and inner hyphens", s.Name)
	}
	info, ok := engines[s.Engine]
	if !ok {
		return invalid("unknown engine %q (available: %s)", s.Engine, strings.Join(Engines(), ", "))
	}
	if s.Resource == "" {
		s.Resource = s.Name
	}
	if !resourceRe.MatchString(s.Resource) {
		return invalid("resource %q is not a valid resource name", s.Resource)
	}
	if s.Vector && s.Engine != Postgres {
		return invalid("--vector (pgvector) is a postgres extension; engine %s has no vector option", s.Engine)
	}
	if s.Port == 0 {
		s.Port = info.defaultPort
	}
	if s.Port < 1 || s.Port > 65535 {
		return invalid("port %d is outside 1-65535", s.Port)
	}
	if _, _, err := ParseServiceIP(s.ServiceIP); err != nil {
		return err
	}
	return nil
}

// ParseServiceIP splits an IPv4 CIDR into the address and prefix length the
// IPaddr2 agent takes. The address must be a host address: the network or
// broadcast address of its subnet would never answer.
func ParseServiceIP(cidr string) (string, int, error) {
	if cidr == "" {
		return "", 0, invalid("a service IP in CIDR notation is required, e.g. 192.168.1.60/24")
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", 0, invalid("service IP %q is not in CIDR notation (e.g. 192.168.1.60/24)", cidr)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return "", 0, invalid("service IP %q is not IPv4; the IPaddr2 agent manages IPv4 addresses", cidr)
	}
	prefix, _ := ipNet.Mask.Size()
	if prefix == 0 {
		return "", 0, invalid("service IP %q has a /0 prefix", cidr)
	}
	if prefix < 31 {
		network := ip4.Mask(ipNet.Mask)
		broadcast := make(net.IP, 4)
		for i := range broadcast {
			broadcast[i] = network[i] | ^ipNet.Mask[i]
		}
		if ip4.Equal(network) || ip4.Equal(broadcast) {
			return "", 0, invalid("service IP %q is the network or broadcast address of its subnet", cidr)
		}
	}
	return ip4.String(), prefix, nil
}

// Layout is where an app keeps its files.
type Layout struct {
	Mount      string // the DRBD volume's mount point
	Data       string // the engine's data directory
	Conf       string // the engine's configuration
	State      string // root-only: credentials and the init marker
	Password   string // the generated password, mode 0600, root only
	ClientConf string // mysql only: client credentials for the tools haify runs
	Marker     string // names the engine that initialized the volume
	Runtime    string // sockets and pid files, created by systemd
}

// LayoutFor returns the paths of app name.
func LayoutFor(name string) Layout {
	m := path.Join(MountBase, name)
	state := path.Join(m, "haify")
	return Layout{
		Mount:      m,
		Data:       path.Join(m, "data"),
		Conf:       path.Join(m, "conf"),
		State:      state,
		Password:   path.Join(state, "password"),
		ClientConf: path.Join(state, "client.cnf"),
		Marker:     path.Join(state, "engine"),
		Runtime:    RuntimeBase + name,
	}
}

// UnitName is the app's systemd service.
func UnitName(name string) string { return "haify-app-" + name + ".service" }

// UnitPath is where UnitName is written on every node.
func UnitPath(name string) string { return path.Join(UnitDir, UnitName(name)) }

// PromoterName is the drbd-reactor config name (drbd-reactorctl takes it).
func PromoterName(name string) string { return "haify-app-" + name }

// PromoterPath is the promoter config file.
func PromoterPath(name string) string {
	return path.Join(ReactorConfigDir, PromoterName(name)+".toml")
}

// ThawUnit is the transient unit that thaws the app if nobody else does.
func ThawUnit(name string) string { return "haify-app-thaw-" + name }

// LockUnit is the transient unit holding MySQL's global read lock.
func LockUnit(name string) string { return "haify-app-lock-" + name }

// DataDevice is the DRBD device of one volume of resource, by the stable
// by-res name drbd-utils' udev rules create.
func DataDevice(resource string, volume int) string {
	return fmt.Sprintf("/dev/drbd/by-res/%s/%d", resource, volume)
}

// Binaries are where an engine's programs live on the nodes. The probe finds
// them, and every node must agree, because one unit file is written to all.
type Binaries struct {
	Server string // postgres, mariadbd/mysqld or redis-server
	Client string // psql, mariadb/mysql or redis-cli
	Admin  string // pg_isready or mariadb-admin/mysqladmin
	Init   string // initdb or mariadb-install-db; MySQL initializes through the server
	Flavor string // mysql engine: "mariadb" or "mysql"
}

// pgCtl is pg_ctl, beside the postgres binary.
func (b Binaries) pgCtl() string { return path.Join(path.Dir(b.Server), "pg_ctl") }

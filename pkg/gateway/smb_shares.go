package gateway

import (
	"bufio"
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// Shares and users of a running SMB gateway.
//
// Both live on the gateway's state volume (smb.go), which only the node
// serving the gateway has mounted, so every change here is made there and
// applied with `smbcontrol reload-config` — smbd re-reads its shares without
// dropping a session. A stopped gateway cannot be changed: start it first.

// SMBShare is one share of an SMB gateway.
type SMBShare struct {
	Name string
	// Path is the share's directory relative to the data volume's root; ""
	// shares the whole volume.
	Path       string
	ReadOnly   bool
	ValidUsers []string
}

var (
	smbShareNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	smbUserRE      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	smbPathRE      = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)
	smbWorkgroupRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,14}$`)
	smbReserved    = map[string]bool{"global": true, "homes": true, "printers": true, "print$": true, "ipc$": true}
)

func (sh *SMBShare) validate() error {
	if !smbShareNameRE.MatchString(sh.Name) || smbReserved[strings.ToLower(sh.Name)] {
		return fmt.Errorf("invalid share name %q: letters, digits, '_', '.', '-', up to 64, not a Samba reserved name", sh.Name)
	}
	if sh.Path != "" {
		clean := path.Clean(sh.Path)
		if !smbPathRE.MatchString(sh.Path) || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(sh.Path, "/") {
			return fmt.Errorf("invalid share path %q: a directory under the gateway's volume, like \"projects/a\"", sh.Path)
		}
		sh.Path = clean
		if sh.Path == "." {
			sh.Path = ""
		}
	}
	for _, u := range sh.ValidUsers {
		if err := validateSMBUser(u); err != nil {
			return err
		}
	}
	return nil
}

func validateSMBUser(u string) error {
	if !smbUserRE.MatchString(u) || u == smbOwner || u == "root" {
		return fmt.Errorf("invalid SMB user %q: lower-case letters, digits, '_' and '-', up to 32, starting with a letter", u)
	}
	return nil
}

func validateSMBWorkgroup(w string) (string, error) {
	if w == "" {
		return "WORKGROUP", nil
	}
	if !smbWorkgroupRE.MatchString(w) {
		return "", fmt.Errorf("invalid workgroup %q: up to 15 letters, digits, '_' or '-'", w)
	}
	return strings.ToUpper(w), nil
}

// section renders the share for shares.conf.
func (sh SMBShare) section(resource string) string {
	ro := "no"
	if sh.ReadOnly {
		ro = "yes"
	}
	lines := []string{
		"[" + sh.Name + "]",
		"  path = " + path.Join(smbShareRoot(resource), sh.Path),
		"  read only = " + ro,
		"  force user = " + smbOwner,
		"  force group = " + smbOwner,
		"  create mask = 0664",
		"  directory mask = 0775",
	}
	if len(sh.ValidUsers) > 0 {
		lines = append(lines, "  valid users = "+strings.Join(sh.ValidUsers, " "))
	}
	return strings.Join(lines, "\n") + "\n"
}

// renderSMBShares renders a whole shares.conf.
func renderSMBShares(resource string, shares []SMBShare) string {
	var b strings.Builder
	b.WriteString("# Haify SMB gateway shares (managed by haify gateway smb share)\n")
	for _, sh := range shares {
		b.WriteString("\n" + sh.section(resource))
	}
	return b.String()
}

// parseSMBShares reads shares.conf back.
func parseSMBShares(resource, content string) []SMBShare {
	var out []SMBShare
	var cur *SMBShare
	root := smbShareRoot(resource)
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			out = append(out, SMBShare{Name: strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")})
			cur = &out[len(out)-1]
		case cur != nil:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch k {
			case "path":
				cur.Path = strings.TrimPrefix(strings.TrimPrefix(v, root), "/")
			case "read only":
				cur.ReadOnly = v == "yes" || v == "true"
			case "valid users":
				cur.ValidUsers = strings.Fields(v)
			}
		}
	}
	return out
}

// servingNode returns the node serving resource's gateway, and every node
// that may serve it.
func (s *SMBManager) servingNode(ctx context.Context, resource string) (string, []string, error) {
	res, err := s.resources.GetResource(ctx, resource)
	if err != nil || res == nil {
		return "", nil, fmt.Errorf("look up %s: %v", resource, err)
	}
	hosts := gatewayNodes(res)
	states, err := s.gatewayTargetStates(ctx, hosts, resource)
	if err != nil {
		return "", nil, err
	}
	host, err := runningGatewayHost(hosts, states, resource)
	if err != nil {
		return "", nil, err
	}
	if host == "" {
		return "", nil, fmt.Errorf("SMB gateway %s is not running; start it first: its shares and users live on its state volume, which only the serving node has mounted", resource)
	}
	return host, hosts, nil
}

// readOn returns what cmd prints on host.
func (s *SMBManager) readOn(ctx context.Context, host, cmd string) (string, error) {
	reader, ok := s.deployment.(HostOutputReader)
	if !ok {
		return "", fmt.Errorf("deployment client cannot read node output")
	}
	out, err := reader.ExecOutput(ctx, []string{host}, scriptCmd(cmd))
	if err != nil {
		return "", err
	}
	text, ok := out[host]
	if !ok {
		return "", fmt.Errorf("%s did not answer", host)
	}
	return text, nil
}

// ListSMBShares lists the shares of a running SMB gateway.
func (s *SMBManager) ListSMBShares(ctx context.Context, resource string) ([]SMBShare, error) {
	host, _, err := s.servingNode(ctx, resource)
	if err != nil {
		return nil, err
	}
	content, err := s.readOn(ctx, host, "cat "+smbSharesPath(resource))
	if err != nil {
		return nil, err
	}
	return parseSMBShares(resource, content), nil
}

// writeShares replaces shares.conf on host, creates the directory of share
// (when given) and has smbd re-read its shares.
func (s *SMBManager) writeShares(ctx context.Context, host, resource string, shares []SMBShare, created *SMBShare) error {
	mk := ""
	if created != nil {
		dir := path.Join(smbShareRoot(resource), created.Path)
		mk = fmt.Sprintf("mkdir -p %[1]s && chown %[2]s:%[2]s %[1]s\n", shq(dir), smbOwner)
	}
	script := fmt.Sprintf(`set -e
%[1]secho %[2]s | base64 -d > %[3]s.new && mv %[3]s.new %[3]s
smbcontrol -s %[4]s smbd reload-config
`, mk, base64Of(renderSMBShares(resource, shares)), smbSharesPath(resource), smbConfPath(resource))
	return s.runScript(ctx, []string{host}, script)
}

// AddSMBShare adds a share to a running SMB gateway.
func (s *SMBManager) AddSMBShare(ctx context.Context, resource string, share SMBShare) error {
	if err := share.validate(); err != nil {
		return invalidArgument(err)
	}
	host, _, err := s.servingNode(ctx, resource)
	if err != nil {
		return err
	}
	content, err := s.readOn(ctx, host, "cat "+smbSharesPath(resource))
	if err != nil {
		return err
	}
	shares := parseSMBShares(resource, content)
	for _, sh := range shares {
		if strings.EqualFold(sh.Name, share.Name) {
			return fmt.Errorf("share %s already exists on %s", share.Name, resource)
		}
	}
	if err := smbNestedExposure(shares, share); err != nil {
		return invalidArgument(err)
	}
	shares = append(shares, share)
	s.logger.Info("Adding SMB share", zap.String("resource", resource), zap.String("share", share.Name))
	return s.writeShares(ctx, host, resource, shares, &share)
}

// smbNestedExposure refuses a share whose directory overlaps another share's
// with a wider set of users. Samba checks valid users per share, not per
// directory, so the files of a share restricted to alice are open to everyone
// a share of its parent directory lets in — the gateway's first share covers
// the whole volume and lets in every user unless it was given --valid-users.
func smbNestedExposure(existing []SMBShare, added SMBShare) error {
	for _, sh := range existing {
		outer, inner := sh, added
		if !smbPathWithin(inner.Path, outer.Path) {
			outer, inner = added, sh
			if !smbPathWithin(inner.Path, outer.Path) {
				continue
			}
		}
		extra := smbUsersBeyond(outer.ValidUsers, inner.ValidUsers)
		if extra == "" {
			continue
		}
		return fmt.Errorf("share %s (/%s) lies inside share %s (/%s), which lets in %s: they would reach %s's files "+
			"through %s. Give %s --valid-users that %s also has (remove and re-add it), or put %s outside it",
			inner.Name, inner.Path, outer.Name, outer.Path, extra, inner.Name, outer.Name, outer.Name, inner.Name, inner.Name)
	}
	return nil
}

// smbPathWithin reports whether dir is base or under it ("" is the volume root).
func smbPathWithin(dir, base string) bool {
	return base == "" || dir == base || strings.HasPrefix(dir, base+"/")
}

// smbUsersBeyond describes who outer lets in that inner does not, or "" when
// outer lets in no one inner does not. An empty list means every user.
func smbUsersBeyond(outer, inner []string) string {
	if len(inner) == 0 {
		return ""
	}
	if len(outer) == 0 {
		return "every user"
	}
	allowed := map[string]bool{}
	for _, u := range inner {
		allowed[u] = true
	}
	var extra []string
	for _, u := range outer {
		if !allowed[u] {
			extra = append(extra, u)
		}
	}
	return strings.Join(extra, ", ")
}

// RemoveSMBShare removes a share; its directory and data are left in place.
func (s *SMBManager) RemoveSMBShare(ctx context.Context, resource, name string) error {
	host, _, err := s.servingNode(ctx, resource)
	if err != nil {
		return err
	}
	content, err := s.readOn(ctx, host, "cat "+smbSharesPath(resource))
	if err != nil {
		return err
	}
	shares := parseSMBShares(resource, content)
	kept := shares[:0]
	for _, sh := range shares {
		if !strings.EqualFold(sh.Name, name) {
			kept = append(kept, sh)
		}
	}
	if len(kept) == len(shares) {
		return fmt.Errorf("share %s not found on %s", name, resource)
	}
	s.logger.Info("Removing SMB share", zap.String("resource", resource), zap.String("share", name))
	return s.writeShares(ctx, host, resource, kept, nil)
}

// SetSMBUser adds an SMB user, or changes its password.
func (s *SMBManager) SetSMBUser(ctx context.Context, resource, user, password string) error {
	if err := validateSMBUser(user); err != nil {
		return invalidArgument(err)
	}
	if password == "" || strings.ContainsAny(password, "\n\r\x00") {
		return invalidArgument(fmt.Errorf("a password is required, on one line"))
	}
	host, hosts, err := s.servingNode(ctx, resource)
	if err != nil {
		return err
	}
	recorded, err := s.recordedUIDs(ctx, host, resource)
	if err != nil {
		return err
	}
	uid, err := s.allocateUID(ctx, hosts, user, recorded)
	if err != nil {
		return err
	}
	conf, users := smbConfPath(resource), smbUsersPath(resource)
	script := fmt.Sprintf(`set -e
grep -q '^%[1]s:' %[2]s || echo '%[1]s:%[3]d' >> %[2]s
%[4]s %[2]s
pw=$(echo %[5]s | base64 -d)
printf '%%s\n%%s\n' "$pw" "$pw" | smbpasswd -c %[6]s -s -a %[1]s >/dev/null
smbpasswd -c %[6]s -e %[1]s >/dev/null
`, user, users, uid, smbUsersHelper, base64Of(password), conf)
	s.logger.Info("Setting SMB user", zap.String("resource", resource), zap.String("user", user))
	return s.runScript(ctx, []string{host}, script)
}

// RemoveSMBUser removes an SMB user. Its local account stays, unused.
func (s *SMBManager) RemoveSMBUser(ctx context.Context, resource, user string) error {
	if err := validateSMBUser(user); err != nil {
		return invalidArgument(err)
	}
	host, _, err := s.servingNode(ctx, resource)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
pdbedit -s %[1]s -L | grep -q '^%[2]s:' || { echo "no SMB user %[2]s" >&2; exit 1; }
smbpasswd -c %[1]s -x %[2]s >/dev/null
grep -v '^%[2]s:' %[3]s > %[3]s.new || true
mv %[3]s.new %[3]s
`, smbConfPath(resource), user, smbUsersPath(resource))
	return s.runScript(ctx, []string{host}, script)
}

// ListSMBUsers lists the SMB users of a running gateway.
func (s *SMBManager) ListSMBUsers(ctx context.Context, resource string) ([]string, error) {
	host, _, err := s.servingNode(ctx, resource)
	if err != nil {
		return nil, err
	}
	out, err := s.readOn(ctx, host, "pdbedit -s "+smbConfPath(resource)+" -L")
	if err != nil {
		return nil, err
	}
	var users []string
	for _, line := range strings.Split(out, "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && name != "" {
			users = append(users, name)
		}
	}
	sort.Strings(users)
	return users, nil
}

// recordedUIDs reads the gateway's users file.
func (s *SMBManager) recordedUIDs(ctx context.Context, host, resource string) (map[string]int, error) {
	out, err := s.readOn(ctx, host, "cat "+smbUsersPath(resource)+" 2>/dev/null; true")
	if err != nil {
		return nil, err
	}
	return parseNameIDs(out, 1), nil
}

// parseNameIDs reads "name:...:id" lines, the id being field idField.
func parseNameIDs(out string, idField int) map[string]int {
	m := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) <= idField || f[0] == "" {
			continue
		}
		if id, err := strconv.Atoi(f[idField]); err == nil {
			m[f[0]] = id
		}
	}
	return m
}

// allocateUID returns the uid name has, or gets, on every host: the one it
// was recorded with, the one it already has on the hosts (which must agree),
// or the lowest id from smbFirstUID that is free as a uid and a gid on all.
func (s *SMBManager) allocateUID(ctx context.Context, hosts []string, name string, recorded map[string]int) (int, error) {
	if uid, ok := recorded[name]; ok {
		return uid, nil
	}
	reader, ok := s.deployment.(HostOutputReader)
	if !ok {
		return 0, fmt.Errorf("deployment client cannot read node output")
	}
	out, err := reader.ExecOutput(ctx, hosts, scriptCmd("getent passwd; echo ---; getent group"))
	if err != nil {
		return 0, fmt.Errorf("read accounts: %w", err)
	}
	if len(out) < len(hosts) {
		return 0, fmt.Errorf("cannot read the accounts on every node of the gateway; retry when they all answer")
	}
	return pickUID(name, out, recorded)
}

// pickUID is allocateUID's decision over each host's getent output.
func pickUID(name string, outputs map[string]string, recorded map[string]int) (int, error) {
	used := map[int]bool{}
	for _, id := range recorded {
		used[id] = true
	}
	have := map[int][]string{}
	takenBy := map[int]string{} // uid -> another account's name holding it somewhere
	for host, out := range outputs {
		passwd, group, _ := strings.Cut(out, "---")
		users := parseNameIDs(passwd, 2)
		if uid, ok := users[name]; ok {
			have[uid] = append(have[uid], host)
		}
		for n, id := range users {
			used[id] = true
			if n != name {
				takenBy[id] = n + " on " + host
			}
		}
		for _, id := range parseNameIDs(group, 2) {
			used[id] = true
		}
	}
	switch len(have) {
	case 0:
	case 1:
		for uid := range have {
			if other, ok := takenBy[uid]; ok {
				return 0, fmt.Errorf("%s has uid %d, which is %s; the gateway could not start there", name, uid, other)
			}
			return uid, nil
		}
	default:
		return 0, fmt.Errorf("%s has different uids on the gateway's nodes (%v); make them agree first", name, have)
	}
	for uid := smbFirstUID; uid < smbFirstUID+4000; uid++ {
		if !used[uid] {
			return uid, nil
		}
	}
	return 0, fmt.Errorf("no free uid from %d on every node", smbFirstUID)
}

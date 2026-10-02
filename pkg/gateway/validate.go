package gateway

import (
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The identifiers a gateway is created with — the IQN, the NQN, the NVMe
// transport type — are never interpreted by the controller. They are copied
// verbatim into the drbd-reactor promoter config and only reach a parser when
// drbd-reactor promotes the resource and hands them to the OCF agents.
//
// That is why they are validated here, at the gateway API boundary, rather than
// in a request handler: by the time an unparseable value would be noticed, the
// DRBD resource exists, the cluster-private state volume has been provisioned,
// and the config file has been distributed to every node — and the failure
// surfaces as a promoter that will not start, nowhere near the request that
// caused it. Rejecting the value costs nothing here and a full gateway teardown
// one step later.
//
// The gateway managers are reached from the CLI, the gRPC server and the MCP
// server alike (all three go through pkg/controller's handlers), so this is the
// single layer that covers every caller.

// invalidArgument reports a rejected caller-supplied value.
//
// The gateway managers' errors are returned straight through pkg/controller's
// gRPC handlers, so an unclassified error reaches the operator as
// codes.Unknown — indistinguishable from a node being unreachable. Tagging it
// InvalidArgument is what tells a caller (and the CLI's error rendering) that
// retrying will not help until the request itself is fixed.
func invalidArgument(err error) error {
	return status.Error(codes.InvalidArgument, err.Error())
}

// iSCSI node name prefixes, from RFC 3720 section 3.2.6.
//
// All three are accepted because LIO accepts all three: a target named
// "eui.0123456789abcdef" works exactly as well as one named "iqn.…", and there
// is no layer below this one that would reject it. Refusing it here would be
// this validator inventing a restriction the storage stack does not have — and
// it briefly did: when the check was first wired into the create path it only
// knew "iqn.", which turned a name that had always worked into a rejected
// request.
const (
	iqnPrefix = "iqn."
	euiPrefix = "eui."
	naaPrefix = "naa."
)

// iscsiNameFormats is the operator-facing description of what validateIQN
// accepts, kept in one place so that every rejection names all three formats.
// A message that mentions only the format the operator did not use reads as
// "your name is malformed" when the truth may be "this tool only understood one
// of the three legal spellings" — which is precisely how the eui. regression
// would have been reported.
const iscsiNameFormats = `accepted formats are ` +
	`"iqn." + date + reversed domain + ":" + local name (iqn.2024-01.com.example:sds.data), ` +
	`"eui." + 16 hex digits (eui.0123456789abcdef), or ` +
	`"naa." + 16 or 32 hex digits (naa.60014051f1b2c3d4)`

// validateIQN validates an iSCSI node name in any of the three formats RFC 3720
// defines. The name is checked structurally, not just by prefix: an "eui." that
// is followed by nine characters of base64 is not an EUI-64 identifier, and
// passing it through would put a name in the target config that no initiator
// can address.
//
// Case: the prefix is matched case-sensitively, in the lowercase form the RFC
// prescribes, for all three formats — that is the rule the iqn.-only version
// already applied. Nothing after the prefix is case-normalised, for any of the
// three, so uppercase hex is accepted exactly as an uppercase domain label in
// an iqn. name always has been. The alternative — folding case for eui./naa.
// but not for iqn. — would make one function answer the same question two
// different ways depending on which prefix the operator happened to pick, which
// is worse than either rule on its own.
func validateIQN(name string) error {
	switch {
	case strings.HasPrefix(name, iqnPrefix):
		// Unchanged from when iqn. was the only accepted format. The rest of an
		// iqn. name is a date, a reversed domain and an operator-chosen local
		// part, none of which this layer can meaningfully judge; the colon is
		// the one piece whose absence makes the name unusable rather than merely
		// unconventional.
		if !strings.Contains(name, ":") {
			return fmt.Errorf("invalid iSCSI name %q: an %q name needs the \":\" separating the "+
				"naming authority from the local name; %s", name, iqnPrefix, iscsiNameFormats)
		}
		return nil
	case strings.HasPrefix(name, euiPrefix):
		return validateHexName(name, euiPrefix, "16", 16)
	case strings.HasPrefix(name, naaPrefix):
		return validateHexName(name, naaPrefix, "16 or 32", 16, 32)
	default:
		return fmt.Errorf("invalid iSCSI name %q: %s", name, iscsiNameFormats)
	}
}

// validateHexName checks the fixed-width hexadecimal body of an eui. or naa.
// name. The permitted lengths are the format itself, not a style preference:
// an EUI-64 identifier is exactly 64 bits and an NAA identifier is 64 or 128,
// so a body one digit short is a typo that would otherwise be copied verbatim
// into the target definition.
func validateHexName(name, prefix, want string, lengths ...int) error {
	body := strings.TrimPrefix(name, prefix)
	for _, r := range body {
		if !isHexDigit(r) {
			return fmt.Errorf("invalid iSCSI name %q: %q must be followed by %s hexadecimal digits, "+
				"but %q is not one; %s", name, prefix, want, string(r), iscsiNameFormats)
		}
	}
	for _, n := range lengths {
		if len(body) == n {
			return nil
		}
	}
	return fmt.Errorf("invalid iSCSI name %q: %q must be followed by %s hexadecimal digits, got %d; %s",
		name, prefix, want, len(body), iscsiNameFormats)
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// validateNQN validates an NQN format
func validateNQN(nqn string) error {
	if !strings.HasPrefix(nqn, "nqn.") {
		return fmt.Errorf("invalid NQN format: must start with 'nqn.'")
	}

	parts := strings.Split(nqn, ":")
	if len(parts) < 2 {
		return fmt.Errorf("invalid NQN format: missing colon separator")
	}

	return nil
}

// parseTransportType parses and validates an NVMe transport type. fc is not
// accepted: the generated nvmet-port line addresses the port by the service
// IP, and an FC port is addressed by WWNN/WWPN.
func parseTransportType(transport string) error {
	validTypes := []string{"tcp", "rdma"}
	for _, t := range validTypes {
		if transport == t {
			return nil
		}
	}
	return fmt.Errorf("invalid transport type: %s (valid: %v)", transport, validTypes)
}

// validateISCSIImplementation normalises the requested iSCSI target
// implementation and refuses the ones the generated config does not support.
//
// "lio" historically meant the long-gone lio_node toolchain; every current
// distribution ships targetcli, which the OCF agents call "lio-t". tgt and iet
// are refused rather than passed through: with either, the iSCSITarget agent
// ignores portals= and listens on every address instead of the service IP, tgt
// needs a tgtd daemon that nothing in the promoter chain starts, and IET is not
// packaged by current distributions. The stop order the chain relies on
// (service IP first, then LUNs, then target) has only been verified on LIO.
func validateISCSIImplementation(implementation string) (string, error) {
	switch implementation {
	case "", "lio", "lio-t":
		return "lio-t", nil
	case "tgt":
		return "", fmt.Errorf("iSCSI implementation \"tgt\" is not supported: SDS gateways run on LIO (targetcli) only; " +
			"with tgt the iSCSITarget agent ignores the service-IP portal and needs a tgtd daemon the promoter chain " +
			"does not start. Use --implementation lio")
	case "iet":
		return "", fmt.Errorf("iSCSI implementation \"iet\" is not supported: SDS gateways run on LIO (targetcli) only; " +
			"with iet the iSCSITarget agent ignores the service-IP portal, and IET is not packaged by current " +
			"distributions. Use --implementation lio")
	default:
		return "", fmt.Errorf("unknown iSCSI implementation %q: SDS gateways run on LIO (targetcli) only; use --implementation lio", implementation)
	}
}

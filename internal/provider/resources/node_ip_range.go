package resources

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// parseIPv4Range parses a node_ip_range value ("start-end") into its two ends.
// Both ends must be plain IPv4 addresses (no spaces, no IPv6, no leading zeros)
// and the range must span at least two addresses (start < end): the panel sizes
// the range as "size - 3", so a single address (and, strictly, anything under
// four) already comes out non-positive; only the single address is rejected here.
func parseIPv4Range(s string) (start, end netip.Addr, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return start, end, fmt.Errorf("must be an IPv4 range as start-end, e.g. 10.0.0.10-10.0.0.20")
	}
	start, err = parseIPv4(parts[0])
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("range start: %w", err)
	}
	end, err = parseIPv4(parts[1])
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("range end: %w", err)
	}
	switch c := start.Compare(end); {
	case c > 0:
		return netip.Addr{}, netip.Addr{}, fmt.Errorf(
			"range is reversed: start %s is greater than end %s (write it as %s-%s)", start, end, end, start)
	case c == 0:
		return netip.Addr{}, netip.Addr{}, fmt.Errorf(
			"range holds a single address (%s): start and end must differ", start)
	}
	return start, end, nil
}

func parseIPv4(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return netip.Addr{}, fmt.Errorf("%q is not a valid IPv4 address", s)
	}
	return a, nil
}

// IPv4Range returns a string validator for node_ip_range: a well-formed IPv4
// "start-end" whose start is strictly below its end. It is purely static — the
// checks that need the local network (CIDR, gateway) live in checkNodeIPRange.
// Null and unknown values are skipped.
func IPv4Range() validator.String {
	return ipv4RangeValidator{}
}

type ipv4RangeValidator struct{}

func (v ipv4RangeValidator) Description(_ context.Context) string {
	return "must be an IPv4 range as start-end (start below end), e.g. 10.0.0.10-10.0.0.20"
}

func (v ipv4RangeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v ipv4RangeValidator) ValidateString(
	_ context.Context,
	req validator.StringRequest,
	resp *validator.StringResponse,
) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, _, err := parseIPv4Range(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid node_ip_range", err.Error()+".")
	}
}

// checkNodeIPRange compares a node_ip_range against the local network it will be
// carved from. It is a pure function over the already-fetched network.
//
// errs are findings the panel would accept and then break on later (range outside
// the network's CIDR, the network's gateway inside the range). warns are
// suspicious but tolerated inputs (the network or broadcast address inside the
// range — the panel skips them when it picks the API endpoint).
func checkNodeIPRange(network client.LocalNetwork, rng string) (errs, warns []string) {
	start, end, err := parseIPv4Range(rng)
	if err != nil {
		return []string{err.Error()}, nil
	}

	prefix, err := netip.ParsePrefix(network.CIDR)
	if err != nil || !prefix.Addr().Is4() {
		// Without a usable CIDR there is nothing to compare against; do not guess.
		return nil, []string{fmt.Sprintf(
			"could not parse the CIDR %q of local network %d, so node_ip_range was not checked against it",
			network.CIDR, network.ID)}
	}
	prefix = prefix.Masked()

	insideCIDR := prefix.Contains(start) && prefix.Contains(end)
	switch {
	case !prefix.Contains(start) && !prefix.Contains(end):
		errs = append(errs, fmt.Sprintf(
			"node_ip_range %s-%s does not lie inside the network CIDR %s", start, end, prefix))
	case !prefix.Contains(start):
		errs = append(errs, fmt.Sprintf(
			"node_ip_range start %s lies outside the network CIDR %s", start, prefix))
	case !prefix.Contains(end):
		errs = append(errs, fmt.Sprintf(
			"node_ip_range end %s lies outside the network CIDR %s", end, prefix))
	}

	if network.Gateway != "" {
		gw, gerr := netip.ParseAddr(network.Gateway)
		switch {
		case gerr != nil:
			warns = append(warns, fmt.Sprintf(
				"could not parse the gateway %q of local network %d, so node_ip_range was not checked against it",
				network.Gateway, network.ID))
		case inRange(gw, start, end):
			errs = append(errs, fmt.Sprintf(
				"the network gateway %s lies inside node_ip_range %s-%s; the range must not contain the gateway",
				gw, start, end))
		}
	}

	// A /31 or /32 has no separate network/broadcast addresses. Skipped when the range
	// already escapes the CIDR: that is an error, and these warnings would only be noise.
	if prefix.Bits() <= 30 && insideCIDR {
		if netAddr := prefix.Addr(); inRange(netAddr, start, end) {
			warns = append(warns, fmt.Sprintf(
				"node_ip_range %s-%s includes the network address %s of %s", start, end, netAddr, prefix))
		}
		if bcast := broadcastAddr(prefix); inRange(bcast, start, end) {
			warns = append(warns, fmt.Sprintf(
				"node_ip_range %s-%s includes the broadcast address %s of %s", start, end, bcast, prefix))
		}
	}
	return errs, warns
}

func inRange(a, start, end netip.Addr) bool {
	return a.Compare(start) >= 0 && a.Compare(end) <= 0
}

// broadcastAddr returns the last address of an IPv4 prefix.
func broadcastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	u := binary.BigEndian.Uint32(b[:]) | (^uint32(0) >> p.Bits())
	binary.BigEndian.PutUint32(b[:], u)
	return netip.AddrFrom4(b)
}

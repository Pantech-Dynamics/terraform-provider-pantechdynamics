package firewallrule

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

// Protocols that carry ports or ICMP fields.
const (
	protoTCP  = "tcp"
	protoUDP  = "udp"
	protoICMP = "icmp"
)

var _ resource.ResourceWithValidateConfig = &Resource{}

// ValidateConfig checks the combinations the backend would reject: ports belong
// to tcp and udp only, ICMP fields to icmp only, and a range must not run
// backwards. Unknown values are skipped, because they are not decided yet.
func (r *Resource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg model
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Protocol.IsUnknown() || cfg.Protocol.IsNull() {
		return
	}
	proto := cfg.Protocol.ValueString()
	hasPorts := proto == protoTCP || proto == protoUDP

	switch {
	case hasPorts && cfg.PortStart.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("port_start"), "Missing port_start",
			fmt.Sprintf("port_start is required for the %s protocol. Set it to a single port, and port_end as well for a range.", proto))
	case !hasPorts:
		for _, f := range []field{{"port_start", cfg.PortStart.IsNull()}, {"port_end", cfg.PortEnd.IsNull()}} {
			if !f.null {
				resp.Diagnostics.AddAttributeError(path.Root(f.name), "Ports not allowed",
					fmt.Sprintf("%s only applies to the tcp and udp protocols, not %s.", f.name, proto))
			}
		}
	}
	if proto != protoICMP {
		for _, f := range []field{{"icmp_type", cfg.ICMPType.IsNull()}, {"icmp_code", cfg.ICMPCode.IsNull()}} {
			if !f.null {
				resp.Diagnostics.AddAttributeError(path.Root(f.name), "ICMP field not allowed",
					fmt.Sprintf("%s only applies to the icmp protocol, not %s.", f.name, proto))
			}
		}
	}
	if known(cfg.PortStart.IsNull(), cfg.PortStart.IsUnknown()) && known(cfg.PortEnd.IsNull(), cfg.PortEnd.IsUnknown()) &&
		cfg.PortEnd.ValueInt64() < cfg.PortStart.ValueInt64() {
		resp.Diagnostics.AddAttributeError(path.Root("port_end"), "Invalid port range",
			fmt.Sprintf("port_end (%d) must not be lower than port_start (%d).", cfg.PortEnd.ValueInt64(), cfg.PortStart.ValueInt64()))
	}
}

// field pairs an attribute name with whether the user left it unset.
type field struct {
	name string
	null bool
}

func known(isNull, isUnknown bool) bool { return !isNull && !isUnknown }

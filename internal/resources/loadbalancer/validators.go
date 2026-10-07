package loadbalancer

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Limits the backend enforces.
const (
	maxCIDRs   = 20
	maxTargets = 50
	maxNameLen = 63
)

// cidrListValidator allows at most maxCIDRs IPv4 CIDRs with their host bits clear.
type cidrListValidator struct{}

func (cidrListValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d IPv4 CIDRs, each with its host bits clear", maxCIDRs)
}

func (v cidrListValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (cidrListValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > maxCIDRs {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many CIDRs",
			fmt.Sprintf("A load balancer takes at most %d source CIDRs, got %d.", maxCIDRs, len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		p, err := netip.ParsePrefix(s.ValueString())
		if err != nil || !p.Addr().Is4() || p.Masked() != p {
			resp.Diagnostics.AddAttributeError(req.Path.AtSetValue(s), "Invalid CIDR",
				fmt.Sprintf("%q is not an IPv4 CIDR with its host bits clear, for example \"203.0.113.0/24\".", s.ValueString()))
		}
	}
}

// instanceIDsValidator allows at most maxTargets instance ids.
type instanceIDsValidator struct{}

func (instanceIDsValidator) Description(context.Context) string {
	return fmt.Sprintf("must be at most %d instance ids starting with vm_", maxTargets)
}

func (v instanceIDsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (instanceIDsValidator) ValidateSet(_ context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > maxTargets {
		resp.Diagnostics.AddAttributeError(req.Path, "Too many targets",
			fmt.Sprintf("A load balancer takes at most %d instances, got %d.", maxTargets, len(elems)))
	}
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		if !strings.HasPrefix(s.ValueString(), "vm_") {
			resp.Diagnostics.AddAttributeError(req.Path.AtSetValue(s), "Invalid instance id",
				fmt.Sprintf("Expected an id starting with \"vm_\", got %q.", s.ValueString()))
		}
	}
}

// privatePortDefault plans an unset private_port as public_port, the backend's
// default. Planning the real value, rather than keeping the old one, means that
// removing private_port from the configuration shows the replacement it causes.
type privatePortDefault struct{}

func (privatePortDefault) Description(context.Context) string {
	return "defaults to public_port"
}

func (v privatePortDefault) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (privatePortDefault) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var public types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("public_port"), &public)...)
	if public.IsNull() {
		return
	}
	resp.PlanValue = public // unknown stays unknown until public_port is known
}

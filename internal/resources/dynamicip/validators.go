package dynamicip

import (
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/resourcekit"
)

const idPrefix = "vm_"

func instanceIDValidator() validator.String {
	return resourcekit.IDPrefix("instance", idPrefix)
}

func scheduleValidator() validator.String {
	return resourcekit.OneOf(
		client.DynamicIPScheduleEvery12Hours,
		client.DynamicIPScheduleDaily,
		client.DynamicIPScheduleWeekly,
		client.DynamicIPScheduleMonthly,
	)
}

func connectionsValidator() validator.String {
	return resourcekit.OneOf(
		client.DynamicIPConnectionsKeep,
		client.DynamicIPConnectionsMove,
	)
}

package webhook_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const (
	resourceAddr   = "pantechdynamics_webhook.test"
	dataSourceAddr = "data.pantechdynamics_webhook_event_types.all"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY. The key must be made by an
// owner or admin, and the organization must have room for one more webhook (10 at
// most). Webhooks are free. Each test webhook carries a tfacc-* description and
// points at example.com, so its deliveries fail harmlessly; every one is deleted
// again, and a sweep removes any a failed run left behind.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// requireAcc skips the test unless TF_ACC is set. It is the first line of every
// acceptance test, because the helpers call the live API before resource.Test
// would skip.
func requireAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
}

func preCheck(t *testing.T) {
	t.Helper()
	for _, env := range []string{"PANTECHDYNAMICS_BASE_URL", "PANTECHDYNAMICS_API_KEY"} {
		if os.Getenv(env) == "" {
			t.Fatalf("%s must be set for acceptance tests", env)
		}
	}
}

// apiClient is a direct client, used to check and tamper with the real backend.
func apiClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(os.Getenv("PANTECHDYNAMICS_BASE_URL"), os.Getenv("PANTECHDYNAMICS_API_KEY"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sweep deletes every webhook a test of this name left behind.
func sweep(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		c := apiClient(t)
		hooks, err := c.ListWebhooks(context.Background())
		if err != nil {
			t.Logf("sweep: listing webhooks: %v", err)
			return
		}
		for _, h := range hooks {
			if strings.HasPrefix(h.Description, name) {
				if err := c.DeleteWebhook(context.Background(), h.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
					t.Logf("sweep: deleting %s: %v", h.ID, err)
				}
			}
		}
	})
}

func config(name, description string, enabled bool, events ...string) string {
	quoted := make([]string, len(events))
	for i, e := range events {
		quoted[i] = fmt.Sprintf("%q", e)
	}
	return fmt.Sprintf(`
resource "pantechdynamics_webhook" "test" {
  url         = "https://example.com/tfacc/%s"
  description = %q
  events      = [%s]
  enabled     = %t
}
`, name, description, strings.Join(quoted, ", "), enabled)
}

// checkDestroyed fails if a webhook with this description prefix still exists.
func checkDestroyed(t *testing.T, name string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		hooks, err := apiClient(t).ListWebhooks(context.Background())
		if err != nil {
			return err
		}
		for _, h := range hooks {
			if strings.HasPrefix(h.Description, name) {
				return fmt.Errorf("webhook %s (%s) still exists after destroy", h.ID, h.Description)
			}
		}
		return nil
	}
}

func captureID(dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceAddr]
		if !ok {
			return fmt.Errorf("resource %s not in state", resourceAddr)
		}
		*dst = rs.Primary.ID
		return nil
	}
}

func checkIDUnchanged(old *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceAddr]
		if !ok {
			return fmt.Errorf("resource %s not in state", resourceAddr)
		}
		if rs.Primary.ID != *old {
			return fmt.Errorf("id changed from %q to %q, expected an in-place update", *old, rs.Primary.ID)
		}
		return nil
	}
}

// Create, re-plan, update every argument in place, import and destroy.
func TestAccWebhook_basicUpdateAndImport(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc-whk")
	sweep(t, name)
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, name),
		Steps: []resource.TestStep{
			{
				Config: config(name, name, true, "invoice.paid", "instance.*"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(resourceAddr, "id", regexp.MustCompile(`^whk_`)),
					resource.TestMatchResourceAttr(resourceAddr, "secret", regexp.MustCompile(`^whsec_`)),
					resource.TestCheckResourceAttr(resourceAddr, "events.#", "2"),
					resource.TestCheckResourceAttr(resourceAddr, "events.0", "invoice.paid"),
					resource.TestCheckResourceAttr(resourceAddr, "events.1", "instance.*"),
					resource.TestCheckResourceAttr(resourceAddr, "enabled", "true"),
					resource.TestCheckNoResourceAttr(resourceAddr, "disabled_reason"),
					resource.TestCheckResourceAttrSet(resourceAddr, "created_at"),
					captureID(&id),
				),
			},
			{
				// A second plan must be empty: the secret stays in state although
				// the API never returns it again.
				Config:   config(name, name, true, "invoice.paid", "instance.*"),
				PlanOnly: true,
			},
			{
				// Every argument changes in place and the secret is kept.
				Config: config(name+"-v2", name+" v2", false, "*"),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkIDUnchanged(&id),
					resource.TestCheckResourceAttr(resourceAddr, "url", "https://example.com/tfacc/"+name+"-v2"),
					resource.TestCheckResourceAttr(resourceAddr, "description", name+" v2"),
					resource.TestCheckResourceAttr(resourceAddr, "events.#", "1"),
					resource.TestCheckResourceAttr(resourceAddr, "events.0", "*"),
					resource.TestCheckResourceAttr(resourceAddr, "enabled", "false"),
					resource.TestCheckResourceAttr(resourceAddr, "disabled_reason", "disabled_by_user"),
					resource.TestMatchResourceAttr(resourceAddr, "secret", regexp.MustCompile(`^whsec_`)),
				),
			},
			{
				// The API cannot return the secret, so an import leaves it null.
				ResourceName:            resourceAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret"},
			},
		},
	})
}

func TestAccWebhook_driftWhenDeletedOutsideTerraform(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc-whk-drift")
	sweep(t, name)
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, name),
		Steps: []resource.TestStep{
			{
				Config: config(name, name, true, "*"),
				Check:  captureID(&id),
			},
			{
				// Delete the webhook behind Terraform's back: Read must drop it
				// from state, so the plan wants to create it again.
				PreConfig: func() {
					if err := apiClient(t).DeleteWebhook(context.Background(), id); err != nil {
						t.Fatalf("deleting webhook out of band: %v", err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config(name, name, true, "*"),
				Check:  resource.TestMatchResourceAttr(resourceAddr, "secret", regexp.MustCompile(`^whsec_`)),
			},
		},
	})
}

// Zero-cost checks: plan-time validation and a wrong import id.
func TestAccWebhook_validation(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config: `
resource "pantechdynamics_webhook" "test" {
  url    = "http://example.com/hooks"
  events = ["*"]
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Invalid webhook URL`),
			},
			{
				Config: `
resource "pantechdynamics_webhook" "test" {
  url    = "https://example.com/hooks"
  events = ["instance.*", "instance.*"]
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Duplicate event`),
			},
			{
				Config: `
resource "pantechdynamics_webhook" "test" {
  url    = "https://example.com/hooks"
  events = ["*"]
}
`,
				ResourceName:  resourceAddr,
				ImportState:   true,
				ImportStateId: "sshk_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Invalid webhook id`),
			},
		},
	})
}

func TestAccWebhookEventTypesDataSource(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{
				Config: `data "pantechdynamics_webhook_event_types" "all" {}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(dataSourceAddr, "id", "webhook_event_types"),
					resource.TestMatchResourceAttr(dataSourceAddr, "event_types.#", regexp.MustCompile(`^[1-9]`)),
					resource.TestCheckTypeSetElemNestedAttrs(dataSourceAddr, "event_types.*", map[string]string{"type": "instance.created", "resource": "instance"}),
				),
			},
		},
	})
}

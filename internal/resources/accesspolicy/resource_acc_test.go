package accesspolicy_test

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
	policyAddr     = "pantechdynamics_access_policy.blocks"
	jsonPolicyAddr = "pantechdynamics_access_policy.json"
	teamAddr       = "pantechdynamics_team.test"
	attachAddr     = "pantechdynamics_access_policy_attachment.managed"
	memberAddr     = "pantechdynamics_team_membership.test"
	settingsAddr   = "pantechdynamics_access_settings.test"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY: an owner's key, since
// some IAM changes are owner-only. Teams, policies and attachments are free.
// Each run names its objects tfacc-*, deletes them, and a sweep removes any a
// failed run left. Two parts are opt-in, because they touch real people or the
// whole organization:
//   - TF_ACC_IAM_USER_ID: a member (not an admin) to put in the test team;
//   - TF_ACC_IAM_SETTINGS=1: set member_baseline to "none" and back to "read".
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
}

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

func apiClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(os.Getenv("PANTECHDYNAMICS_BASE_URL"), os.Getenv("PANTECHDYNAMICS_API_KEY"), "test")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sweep deletes the test's policies and teams if a failed run left them.
func sweep(t *testing.T, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		c, bg := apiClient(t), context.Background()
		own := false
		if policies, err := c.ListIAMPolicies(bg, &own); err == nil {
			for _, p := range policies {
				if strings.HasPrefix(p.Name, prefix) {
					if err := c.DeleteIAMPolicy(bg, p.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
						t.Logf("sweep: deleting %s: %v", p.ID, err)
					}
				}
			}
		}
		if teams, err := c.ListIAMTeams(bg); err == nil {
			for _, tm := range teams {
				if strings.HasPrefix(tm.Name, prefix) {
					if err := c.DeleteIAMTeam(bg, tm.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
						t.Logf("sweep: deleting %s: %v", tm.ID, err)
					}
				}
			}
		}
	})
}

func config(name string, actions string, userID string) string {
	membership := ""
	if userID != "" {
		membership = fmt.Sprintf(`
resource "pantechdynamics_team_membership" "test" {
  team_id = pantechdynamics_team.test.id
  user_id = %q
}
`, userID)
	}
	return fmt.Sprintf(`
resource "pantechdynamics_team" "test" {
  name        = "%[1]s-team"
  description = "tfacc"
}

resource "pantechdynamics_access_policy" "blocks" {
  name = "%[1]s-staging-operators"

  statement {
    sid       = "RestartStaging"
    effect    = "allow"
    actions   = [%[2]s]
    resources = ["name:staging-*"]
    conditions = {
      source_ip = ["102.89.1.2", "10.20.30.40/8"]
    }
  }

  statement {
    effect    = "deny"
    actions   = ["instances:Delete"]
    resources = ["name:prod-*"]
  }
}

data "pantechdynamics_access_policy_document" "ci" {
  statement {
    effect    = "allow"
    actions   = ["registry:Push", "registry:Pull", "registry:Read"]
    resources = ["name:api/server"]
  }
}

resource "pantechdynamics_access_policy" "json" {
  name        = "%[1]s-ci"
  policy_json = data.pantechdynamics_access_policy_document.ci.json
}

data "pantechdynamics_access_policy" "developer" {
  id = "pol_pantech_developer"
}

resource "pantechdynamics_access_policy_attachment" "managed" {
  policy_id      = data.pantechdynamics_access_policy.developer.id
  principal_type = "team"
  principal_id   = pantechdynamics_team.test.id
}

resource "pantechdynamics_access_policy_attachment" "own" {
  policy_id      = pantechdynamics_access_policy.blocks.id
  principal_type = "team"
  principal_id   = pantechdynamics_team.test.id
}

data "pantechdynamics_iam_actions" "instances" {
  service = "instances"
}
%[3]s`, name, actions, membership)
}

func attr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes[key], nil
}

func checkDestroyed(t *testing.T, prefix string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		own := false
		policies, err := apiClient(t).ListIAMPolicies(context.Background(), &own)
		if err != nil {
			return err
		}
		for _, p := range policies {
			if strings.HasPrefix(p.Name, prefix) {
				return fmt.Errorf("policy %s still exists after destroy", p.ID)
			}
		}
		return nil
	}
}

// Teams, policies in both forms, attachments (own and managed), the data
// sources, a clean re-plan, an in-place change guarded by If-Match, every
// import form, drift and destroy.
func TestAccIAM_teamsPoliciesAndAttachments(t *testing.T) {
	requireAcc(t)
	name := acctest.RandomWithPrefix("tfacc")
	sweep(t, "tfacc")
	userID := os.Getenv("TF_ACC_IAM_USER_ID")
	var policyID string

	steps := []resource.TestStep{
		{
			Config: config(name, `"instances:Stop", "instances:Start", "instances:Read"`, userID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestMatchResourceAttr(teamAddr, "id", regexp.MustCompile(`^team_`)),
				resource.TestMatchResourceAttr(policyAddr, "id", regexp.MustCompile(`^pol_`)),
				resource.TestCheckResourceAttr(policyAddr, "statement.#", "2"),
				resource.TestMatchResourceAttr(policyAddr, "policy_json", regexp.MustCompile(`"10.0.0.0/8"`)),
				resource.TestCheckResourceAttrPair(jsonPolicyAddr, "policy_json", "data.pantechdynamics_access_policy_document.ci", "json"),
				resource.TestCheckResourceAttr("data.pantechdynamics_access_policy.developer", "managed", "true"),
				resource.TestMatchResourceAttr(attachAddr, "id", regexp.MustCompile(`^pol_pantech_developer/team/team_`)),
				resource.TestMatchResourceAttr("data.pantechdynamics_iam_actions.instances", "actions.#", regexp.MustCompile(`^[1-9]`)),
				func(s *terraform.State) error {
					id, err := attr(s, policyAddr, "id")
					policyID = id
					return err
				},
			),
		},
		// The API stores 102.89.1.2 as 102.89.1.2/32 and 10.20.30.40/8 as
		// 10.0.0.0/8; the configuration must still plan clean.
		{Config: config(name, `"instances:Stop", "instances:Start", "instances:Read"`, userID), PlanOnly: true},
		// Reordered actions mean the same: still clean.
		{Config: config(name, `"instances:Read", "instances:Start", "instances:Stop"`, userID), PlanOnly: true},
		{
			Config: config(name, `"instances:Stop", "instances:Start", "instances:Read", "instances:Reboot"`, userID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPtr(policyAddr, "id", &policyID),
				resource.TestCheckResourceAttr(policyAddr, "version", "2"),
			),
		},
		{ResourceName: teamAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"member_count"}},
		{ResourceName: attachAddr, ImportState: true, ImportStateVerify: true},
		{ResourceName: policyAddr, ImportState: true, ImportStateVerify: true,
			// The import holds the stored CIDRs (102.89.1.2/32, 10.0.0.0/8).
			ImportStateVerifyIgnore: []string{"statement.0.conditions.source_ip", "attachment_count"}},
		{
			// Delete the blocks policy behind Terraform's back: Read drops it
			// (and its attachment), and the plan wants them back.
			PreConfig: func() {
				if err := apiClient(t).DeleteIAMPolicy(context.Background(), policyID); err != nil {
					t.Fatalf("deleting policy out of band: %v", err)
				}
			},
			RefreshState:       true,
			ExpectNonEmptyPlan: true,
		},
		{Config: config(name, `"instances:Stop", "instances:Start", "instances:Read", "instances:Reboot"`, userID)},
	}
	if userID != "" {
		steps = append(steps, resource.TestStep{ResourceName: memberAddr, ImportState: true, ImportStateVerify: true})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, name),
		Steps:                    steps,
	})
}

// member_baseline is organization-wide, so this runs only when asked, and
// ends with the default restored.
func TestAccIAM_accessSettings(t *testing.T) {
	requireAcc(t)
	if os.Getenv("TF_ACC_IAM_SETTINGS") != "1" {
		t.Skip("set TF_ACC_IAM_SETTINGS=1 to change member_baseline (it ends back at \"read\")")
	}
	cfg := func(b string) string {
		return fmt.Sprintf("resource \"pantechdynamics_access_settings\" \"test\" {\n  member_baseline = %q\n}\n", b)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy: func(*terraform.State) error {
			s, err := apiClient(t).GetAccessSettings(context.Background())
			if err != nil {
				return err
			}
			if s.MemberBaseline != "read" {
				return fmt.Errorf("member_baseline = %q after destroy, want it left at \"read\"", s.MemberBaseline)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: cfg("none"), Check: resource.TestCheckResourceAttr(settingsAddr, "member_baseline", "none")},
			{ResourceName: settingsAddr, ImportState: true, ImportStateId: "access_settings", ImportStateVerify: true},
			{Config: cfg("read"), Check: resource.TestCheckResourceAttr(settingsAddr, "member_baseline", "read")},
		},
	})
}

// Zero-cost checks: everything refused at plan time.
func TestAccIAM_validation(t *testing.T) {
	requireAcc(t)
	policy := func(body string) string {
		return "resource \"pantechdynamics_access_policy\" \"blocks\" {\n  name = \"tfacc-v\"\n" + body + "\n}\n"
	}
	statement := func(actions, resources, extra string) string {
		return fmt.Sprintf("  statement {\n    effect = \"allow\"\n    actions = [%s]\n    resources = [%s]\n%s\n  }", actions, resources, extra)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: policy(""), PlanOnly: true, ExpectError: regexp.MustCompile(`Set exactly one of statement and`)},
			{Config: policy(statement(`"Instances:Stop"`, `"*"`, "")), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid access policy`)},
			{Config: policy(statement(`"instances:Stop"`, `"prod-api"`, "")), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid access policy`)},
			{Config: policy(statement(`"instances:Stop"`, `"*"`, `    conditions = { source_ip = ["10.0.0.0/33"] }`)), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid access policy`)},
			{Config: policy(`  policy_json = "{\"statements\": [{\"effect\": \"allow\", \"action\": [\"*\"]}]}"`), PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid policy JSON`)},
			{Config: `resource "pantechdynamics_access_policy" "blocks" {
  name = "pantech_mine"
  policy_json = "{\"statements\": [{\"effect\": \"allow\", \"actions\": [\"*\"], \"resources\": [\"*\"]}]}"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid policy name`)},
			{Config: `resource "pantechdynamics_access_policy_attachment" "managed" {
  policy_id      = "pol_1"
  principal_type = "member"
  principal_id   = "team_1"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid principal id`)},
			{Config: `resource "pantechdynamics_access_settings" "test" { member_baseline = "write" }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid value`)},
			{
				Config: `resource "pantechdynamics_access_policy" "blocks" {
  name = "tfacc-v"
  policy_json = "{\"statements\": [{\"effect\": \"allow\", \"actions\": [\"*\"], \"resources\": [\"*\"]}]}"
}`,
				ResourceName:  policyAddr,
				ImportState:   true,
				ImportStateId: "pol_pantech_developer",
				ExpectError:   regexp.MustCompile(`Cannot import a managed policy`),
			},
		},
	})
}

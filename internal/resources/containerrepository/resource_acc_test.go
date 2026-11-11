package containerrepository_test

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
	namespaceAddr = "pantechdynamics_container_registry_namespace.test"
	repoAddr      = "pantechdynamics_container_repository.test"
	tokenAddr     = "pantechdynamics_container_registry_token.test"
)

// Acceptance tests run only with TF_ACC=1, against the staging API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY (an owner's or admin's
// key with the write and delete scopes). Registry objects are free.
//
// An organization has one namespace, and a claimed namespace cannot be
// released, so the full test runs only when TF_ACC_REGISTRY_NAMESPACE names the
// namespace to claim, or the one the test organization already holds (it is
// adopted, not renamed). The test creates one tfacc-* repository and one
// registry token, deletes both, and a sweep removes any a failed run left.
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

// sweep deletes the test's repositories and tokens if a failed run left them.
func sweep(t *testing.T, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		c, bg := apiClient(t), context.Background()
		if repos, err := c.ListRepositories(bg, prefix); err == nil {
			for _, r := range repos {
				if strings.HasPrefix(r.Name, prefix) {
					if err := c.DeleteRepository(bg, r.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
						t.Logf("sweep: deleting %s: %v", r.ID, err)
					}
				}
			}
		}
		if tokens, err := c.ListRegistryTokens(bg); err == nil {
			for _, tok := range tokens {
				if strings.HasPrefix(tok.Name, prefix) && tok.RevokedAt == nil {
					if err := c.DeleteRegistryToken(bg, tok.ID); err != nil && !errors.Is(err, client.ErrNotFound) {
						t.Logf("sweep: revoking %s: %v", tok.ID, err)
					}
				}
			}
		}
	})
}

func config(namespace, repo, visibility string, retention bool) string {
	ret := ""
	if retention {
		ret = `
  retention = {
    keep_last    = 20
    keep_pattern = "v*"
  }`
	}
	return fmt.Sprintf(`
resource "pantechdynamics_container_registry_namespace" "test" {
  name = %q
}

resource "pantechdynamics_container_repository" "test" {
  name        = %q
  description = "tfacc"
  visibility  = %q
%s
  depends_on = [pantechdynamics_container_registry_namespace.test]
}

resource "pantechdynamics_container_registry_token" "test" {
  name         = %q
  permission   = "pull"
  repositories = [pantechdynamics_container_repository.test.name]
  expires_at   = "2030-01-01T01:00:00+01:00"
}

data "pantechdynamics_container_repository" "by_name" {
  name       = pantechdynamics_container_repository.test.name
  depends_on = [pantechdynamics_container_repository.test]
}

data "pantechdynamics_container_repositories" "all" {
  depends_on = [pantechdynamics_container_repository.test]
}
`, namespace, repo, visibility, ret, repo)
}

func attr(s *terraform.State, addr, key string) (string, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes[key], nil
}

func checkRepoDeleted(t *testing.T, name string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		repos, err := apiClient(t).ListRepositories(context.Background(), name)
		if err != nil {
			return err
		}
		for _, r := range repos {
			if r.Name == name && r.Status != client.RepositoryDeleting {
				return fmt.Errorf("repository %s (%s) still exists after destroy", r.ID, r.Status)
			}
		}
		return nil
	}
}

// Namespace, repository and token end to end: create, a clean re-plan, an
// in-place change, every import form, the data sources, drift and destroy.
func TestAccRegistry_namespaceRepositoryAndToken(t *testing.T) {
	requireAcc(t)
	namespace := os.Getenv("TF_ACC_REGISTRY_NAMESPACE")
	if namespace == "" {
		t.Skip("set TF_ACC_REGISTRY_NAMESPACE to the namespace to claim or adopt: a claimed namespace cannot be released")
	}
	repo := acctest.RandomWithPrefix("tfacc")
	sweep(t, "tfacc")
	var repoID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkRepoDeleted(t, repo),
		Steps: []resource.TestStep{
			{
				Config: config(namespace, repo, "private", true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(namespaceAddr, "id", namespace),
					resource.TestCheckResourceAttrSet(namespaceAddr, "registry_host"),
					resource.TestMatchResourceAttr(repoAddr, "id", regexp.MustCompile(`^crr_`)),
					resource.TestCheckResourceAttr(repoAddr, "namespace", namespace),
					resource.TestMatchResourceAttr(repoAddr, "full_name", regexp.MustCompile("/"+namespace+"/"+repo+"$")),
					resource.TestCheckResourceAttr(repoAddr, "retention.keep_last", "20"),
					resource.TestCheckResourceAttr(repoAddr, "status", client.RepositoryActive),
					resource.TestMatchResourceAttr(tokenAddr, "id", regexp.MustCompile(`^prt_`)),
					resource.TestMatchResourceAttr(tokenAddr, "secret", regexp.MustCompile(`^prt_`)),
					resource.TestCheckResourceAttr(tokenAddr, "expires_at", "2030-01-01T01:00:00+01:00"),
					resource.TestCheckResourceAttrPair("data.pantechdynamics_container_repository.by_name", "id", repoAddr, "id"),
					resource.TestMatchResourceAttr("data.pantechdynamics_container_repositories.all", "repositories.#", regexp.MustCompile(`^[1-9]`)),
					func(s *terraform.State) error {
						id, err := attr(s, repoAddr, "id")
						repoID = id
						return err
					},
				),
			},
			{Config: config(namespace, repo, "private", true), PlanOnly: true},
			{
				// In place: visibility, and removing the retention policy.
				Config: config(namespace, repo, "public", false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(repoAddr, "id", &repoID),
					resource.TestCheckResourceAttr(repoAddr, "visibility", "public"),
					resource.TestCheckNoResourceAttr(repoAddr, "retention"),
				),
			},
			{ResourceName: repoAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "tag_count", "storage_bytes", "last_pushed_at"}},
			{ResourceName: repoAddr, ImportState: true, ImportStateId: repo, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts", "tag_count", "storage_bytes", "last_pushed_at"}},
			{ResourceName: namespaceAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"repository_count", "storage_bytes"}},
			{ResourceName: tokenAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"secret", "last_used_at", "expires_at"}}, // the import holds expires_at in UTC
			{
				// Delete the repository behind Terraform's back: Read drops it.
				PreConfig: func() {
					c := apiClient(t)
					if err := c.DeleteRepository(context.Background(), repoID); err != nil {
						t.Fatalf("deleting repository out of band: %v", err)
					}
					if err := c.WaitUntil(context.Background(), "repository gone", func(ctx context.Context) (bool, error) {
						_, err := c.GetRepository(ctx, repoID)
						return errors.Is(err, client.ErrNotFound), nil
					}); err != nil {
						t.Fatal(err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config(namespace, repo, "public", false),
				Check:  resource.TestCheckResourceAttr(repoAddr, "status", client.RepositoryActive),
			},
		},
	})
}

// Zero-cost checks: everything refused at plan time, and wrong import ids.
func TestAccRegistry_validation(t *testing.T) {
	requireAcc(t)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{
			{Config: `resource "pantechdynamics_container_registry_namespace" "test" { name = "library" }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid registry namespace`)},
			{Config: `resource "pantechdynamics_container_registry_namespace" "test" { name = "Acme_Corp" }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid registry namespace`)},
			{Config: `resource "pantechdynamics_container_repository" "test" { name = "API/Server" }`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid repository name`)},
			{Config: `
resource "pantechdynamics_container_repository" "test" {
  name      = "api"
  retention = {}
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Empty retention policy`)},
			{Config: `
resource "pantechdynamics_container_registry_token" "test" {
  name       = "ci"
  permission = "admin"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid value`)},
			{Config: `
resource "pantechdynamics_container_registry_token" "test" {
  name       = "ci"
  permission = "pull"
  expires_at = "next year"
}`, PlanOnly: true, ExpectError: regexp.MustCompile(`Invalid expiry`)},
			{
				Config: `resource "pantechdynamics_container_registry_token" "test" {
  name       = "ci"
  permission = "pull"
}`,
				ResourceName:  tokenAddr,
				ImportState:   true,
				ImportStateId: "crr_wrong_prefix",
				ExpectError:   regexp.MustCompile(`Invalid registry token id`),
			},
			{
				Config:        `resource "pantechdynamics_container_repository" "test" { name = "api" }`,
				ResourceName:  repoAddr,
				ImportState:   true,
				ImportStateId: "Not A Name",
				ExpectError:   regexp.MustCompile(`Invalid import id`),
			},
		},
	})
}

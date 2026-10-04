package sshkey_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/client"
	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

const resourceAddr = "pantechdynamics_ssh_key.test"

// Acceptance tests run only with TF_ACC=1, against the dev API, using
// PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY. Every key they create is
// named tfacc-* and removed again, and they never touch other keys.
var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
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

// newPublicKey returns a fresh, valid OpenSSH ed25519 public key. The backend
// rejects a public key that is already registered, so each test needs its own.
func newPublicKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const keyType = "ssh-ed25519"
	var wire []byte
	for _, field := range [][]byte{[]byte(keyType), pub} {
		wire = binary.BigEndian.AppendUint32(wire, uint32(len(field)))
		wire = append(wire, field...)
	}
	return keyType + " " + base64.StdEncoding.EncodeToString(wire) + " " + comment
}

func config(name, publicKey string) string {
	if publicKey == "" {
		return fmt.Sprintf(`
resource "pantechdynamics_ssh_key" "test" {
  name = %q
}
`, name)
	}
	return fmt.Sprintf(`
resource "pantechdynamics_ssh_key" "test" {
  name       = %q
  public_key = %q
}
`, name, publicKey)
}

// checkDestroyed fails if a key with this name is still on the account.
func checkDestroyed(t *testing.T, name *string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		keys, err := apiClient(t).ListSSHKeys(context.Background())
		if err != nil {
			return err
		}
		for _, k := range keys {
			if k.Name == *name {
				return fmt.Errorf("ssh key %s (%s) still exists after destroy", k.Name, k.ID)
			}
		}
		return nil
	}
}

func TestAccSSHKey_basicAndReplaceAndImport(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc")
	renamed := name + "-renamed"
	pub := newPublicKey(t, "acc")
	var firstID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &renamed),
		Steps: []resource.TestStep{
			{
				Config: config(name, pub),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "name", name),
					resource.TestCheckResourceAttr(resourceAddr, "public_key", pub),
					resource.TestMatchResourceAttr(resourceAddr, "id", regexpPrefix("sshk_")),
					resource.TestCheckResourceAttrSet(resourceAddr, "fingerprint"),
					resource.TestCheckResourceAttrSet(resourceAddr, "created_at"),
					resource.TestCheckNoResourceAttr(resourceAddr, "private_key"),
					captureID(&firstID),
				),
			},
			{
				// Import by id, and the imported state must match what apply saved.
				ResourceName:            resourceAddr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"private_key"},
			},
			{
				// A name change cannot be updated in place: the key is replaced.
				Config: config(renamed, pub),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceAddr, "name", renamed),
					checkIDChanged(&firstID),
				),
			},
		},
	})
}

func TestAccSSHKey_generatedKeypair(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-gen")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{
				Config: config(name, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceAddr, "public_key"),
					resource.TestMatchResourceAttr(resourceAddr, "private_key", regexpPrefix("-----BEGIN")),
				),
			},
			{
				// A second plan must be empty: the private key stays in state
				// even though the API never returns it again.
				Config:   config(name, ""),
				PlanOnly: true,
			},
		},
	})
}

func TestAccSSHKey_driftWhenDeletedOutsideTerraform(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-drift")
	pub := newPublicKey(t, "drift")
	var id string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{
				Config: config(name, pub),
				Check:  captureID(&id),
			},
			{
				// Delete the key behind Terraform's back, then refresh: Read must
				// drop it from state, so the plan wants to create it again.
				PreConfig: func() {
					if err := apiClient(t).DeleteSSHKey(context.Background(), id); err != nil {
						t.Fatalf("deleting key out of band: %v", err)
					}
				},
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applying again recreates it.
				Config: config(name, pub),
				Check:  resource.TestCheckResourceAttrSet(resourceAddr, "id"),
			},
		},
	})
}

func TestAccSSHKey_importRejectsWrongID(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-imp")
	pub := newPublicKey(t, "imp")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{Config: config(name, pub)},
			{
				ResourceName:  resourceAddr,
				ImportState:   true,
				ImportStateId: "key_wrong_prefix",
				ExpectError:   regexpMust(`Expected an id starting with "sshk_"`),
			},
		},
	})
}

func TestAccSSHKey_duplicateNameIsReported(t *testing.T) {
	name := acctest.RandomWithPrefix("tfacc-dup")
	pub := newPublicKey(t, "dup")
	var existingID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: protoV6Factories,
		CheckDestroy:             checkDestroyed(t, &name),
		Steps: []resource.TestStep{
			{
				// Another key already uses the name, registered out of band.
				PreConfig: func() {
					k, err := apiClient(t).CreateSSHKey(context.Background(), client.CreateSSHKeyRequest{Name: name, PublicKey: newPublicKey(t, "other")})
					if err != nil {
						t.Fatalf("seeding key: %v", err)
					}
					existingID = k.ID
				},
				Config:      config(name, pub),
				ExpectError: regexpMust(`SSH key name already in use`),
			},
			{
				// Remove the seeded key so the final destroy check passes.
				PreConfig: func() {
					if err := apiClient(t).DeleteSSHKey(context.Background(), existingID); err != nil && !errors.Is(err, client.ErrNotFound) {
						t.Fatalf("cleaning up seeded key: %v", err)
					}
				},
				Config: config(name+"-ok", newPublicKey(t, "ok")),
			},
		},
	})
}

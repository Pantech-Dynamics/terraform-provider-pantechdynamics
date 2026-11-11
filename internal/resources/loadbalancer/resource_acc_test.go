package loadbalancer_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
)

// Zero-cost checks of the https listener rules, all refused at plan time, so
// nothing is created and nothing is billed. Run with TF_ACC=1.
func TestAccLoadBalancer_httpsValidation(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	factories := map[string]func() (tfprotov6.ProviderServer, error){
		"pantechdynamics": providerserver.NewProtocol6WithError(provider.New("test")()),
	}
	lb := func(body string) string {
		return "resource \"pantechdynamics_load_balancer\" \"test\" {\n  name = \"tfacc-lb\"\n" + body + "\n}\n"
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: lb(`  public_ip_id = "pip_1"
  subnet_id    = "snet_1"
  public_port  = 443
  protocol     = "https"
  certificate_id = "cert_1"`), PlanOnly: true, ExpectError: regexp.MustCompile(`HTTPS not supported here`)},
			{Config: lb(`  zone_id     = "af-abj-1"
  public_port = 443
  protocol    = "https"`), PlanOnly: true, ExpectError: regexp.MustCompile(`Missing certificate_id`)},
			{Config: lb(`  zone_id        = "af-abj-1"
  public_port    = 443
  certificate_id = "cert_1"`), PlanOnly: true, ExpectError: regexp.MustCompile(`HTTPS setting on a tcp listener`)},
			{Config: lb(`  zone_id        = "af-abj-1"
  public_port    = 80
  protocol       = "https"
  certificate_id = "cert_1"
  redirect_http  = true`), PlanOnly: true, ExpectError: regexp.MustCompile(`Port 80 is the redirect`)},
		},
	})
}

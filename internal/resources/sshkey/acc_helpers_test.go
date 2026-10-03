package sshkey_test

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func regexpPrefix(prefix string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(prefix))
}

func regexpMust(expr string) *regexp.Regexp { return regexp.MustCompile(expr) }

// captureID stores the resource's id so a later step can compare or use it.
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

// checkIDChanged proves a replacement happened rather than an in-place update.
func checkIDChanged(old *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceAddr]
		if !ok {
			return fmt.Errorf("resource %s not in state", resourceAddr)
		}
		if strings.TrimSpace(*old) == "" || rs.Primary.ID == *old {
			return fmt.Errorf("id %q did not change, expected a replacement", rs.Primary.ID)
		}
		return nil
	}
}

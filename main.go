package main

import (
	"context"
	"flag"
	"log"

	"github.com/Pantech-Dynamics/terraform-provider-pantechdynamics/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is set by goreleaser at build time.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers like delve")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/pantechdynamics/pantechdynamics",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}

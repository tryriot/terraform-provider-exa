package provider

import (
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/tryriot/terraform-provider-exa/internal/exa"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"exa": providerserver.NewProtocol6WithError(New("test")()),
}

func testAccPreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv(APIKeyEnvVar) == "" {
		t.Fatal(APIKeyEnvVar + " must be set to an Exa service key for acceptance tests")
	}
}

func testAccClient() *exa.Client {
	return &exa.Client{BaseURL: exa.DefaultBaseURL, APIKey: os.Getenv(APIKeyEnvVar)}
}

func TestProviderRequiresAServiceKey(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      `resource "exa_api_key" "test" { name = "never-created" }`,
				ExpectError: regexp.MustCompile(`Missing Exa service key`),
			},
		},
	})
}

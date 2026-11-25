package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	sdkacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/tryriot/terraform-provider-exa/internal/exa"
)

// Each scenario runs against the in-memory fake (TestAPIKey*, in CI) and the
// real API (TestAccAPIKey*, TF_ACC only). On the real API, only ever touch the
// tf-acc-* keys these tests create.

const apiKeyAddr = "exa_api_key.test"

type target struct {
	preCheck  func()
	factories map[string]func() (tfprotov6.ProviderServer, error)
	client    *exa.Client
}

func realExa(t *testing.T) target {
	return target{
		preCheck:  func() { testAccPreCheck(t) },
		factories: testAccProtoV6ProviderFactories,
		client:    testAccClient(),
	}
}

func TestAPIKeyResource(t *testing.T) {
	resource.UnitTest(t, apiKeyLifecycle(newFakeExa(t, false).target()))
}

func TestAccAPIKeyResource(t *testing.T) {
	resource.Test(t, apiKeyLifecycle(realExa(t)))
}

func TestAPIKeyResource_rotation(t *testing.T) {
	resource.UnitTest(t, apiKeyRotation(newFakeExa(t, false).target()))
}

func TestAccAPIKeyResource_rotation(t *testing.T) {
	resource.Test(t, apiKeyRotation(realExa(t)))
}

func TestAPIKeyResource_budget(t *testing.T) {
	resource.UnitTest(t, apiKeyBudget(newFakeExa(t, true).target()))
}

func TestAccAPIKeyResource_budget(t *testing.T) {
	if os.Getenv("EXA_ACC_BUDGETS") == "" {
		t.Skip("EXA_ACC_BUDGETS not set: the team needs the Exa API key budgets feature")
	}
	resource.Test(t, apiKeyBudget(realExa(t)))
}

func TestAPIKeyResource_recreatedWhenDeletedOutsideTerraform(t *testing.T) {
	fake := newFakeExa(t, false)
	tg := fake.target()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: tg.factories,
		CheckDestroy:             checkAPIKeysDestroyed(tg.client),
		Steps: []resource.TestStep{
			{Config: apiKeyConfig("tf-acc-deleted", "")},
			{
				PreConfig: fake.deleteAll,
				Config:    apiKeyConfig("tf-acc-deleted", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionCreate)},
				},
			},
		},
	})
}

func TestAPIKeyResource_rejectsNonPositiveRateLimit(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: newFakeExa(t, false).target().factories,
		Steps: []resource.TestStep{
			{
				Config:      apiKeyConfig("tf-acc-never-created", "rate_limit = -1"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must be at least 1`),
			},
		},
	})
}

func TestAPIKeyResource_rotationNeedsGracePeriod(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: newFakeExa(t, false).target().factories,
		Steps: []resource.TestStep{
			{
				Config:      apiKeyConfig("tf-acc-never-created", `keepers = { version = "1" }`),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`rotation_grace_period_seconds`),
			},
		},
	})
}

func apiKeyLifecycle(tg target) resource.TestCase {
	name := sdkacctest.RandomWithPrefix("tf-acc")
	sameKey := statecheck.CompareValue(compare.ValuesSame())
	replacedKey := statecheck.CompareValue(compare.ValuesDiffer())

	return resource.TestCase{
		PreCheck:                 tg.preCheck,
		ProtoV6ProviderFactories: tg.factories,
		CheckDestroy:             checkAPIKeysDestroyed(tg.client),
		Steps: []resource.TestStep{
			{
				Config: apiKeyConfig(name, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("id"), knownvalue.StringRegexp(regexp.MustCompile(`^key_`))),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("rate_limit"), knownvalue.Null()),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("budget_cents"), knownvalue.Null()),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("secret"), knownvalue.StringRegexp(regexp.MustCompile(`.{16,}`))),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("team_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("created_at"), knownvalue.NotNull()),
					sameKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
				},
			},
			{
				ResourceName:      apiKeyAddr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: apiKeyConfig(name+"-renamed", "rate_limit = 10"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("name"), knownvalue.StringExact(name+"-renamed")),
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("rate_limit"), knownvalue.Int64Exact(10)),
					sameKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
				},
			},
			{
				Config: apiKeyConfig(name+"-renamed", "rate_limit = 20"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("rate_limit"), knownvalue.Int64Exact(20)),
					sameKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
					replacedKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
				},
			},
			{
				Config: apiKeyConfig(name+"-renamed", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("rate_limit"), knownvalue.Null()),
					replacedKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
				},
			},
		},
	}
}

func apiKeyRotation(tg target) resource.TestCase {
	name := sdkacctest.RandomWithPrefix("tf-acc")
	sameKey := statecheck.CompareValue(compare.ValuesSame())
	newSecret := statecheck.CompareValue(compare.ValuesDiffer())

	return resource.TestCase{
		PreCheck:                 tg.preCheck,
		ProtoV6ProviderFactories: tg.factories,
		CheckDestroy:             checkAPIKeysDestroyed(tg.client),
		Steps: []resource.TestStep{
			{
				Config: apiKeyRotationConfig(name, "1"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("previous_secret_expires_at"), knownvalue.Null()),
					sameKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
					newSecret.AddStateValue(apiKeyAddr, tfjsonpath.New("secret")),
				},
			},
			{
				Config: apiKeyRotationConfig(name, "2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(apiKeyAddr, tfjsonpath.New("secret")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("previous_secret_expires_at"), knownvalue.NotNull()),
					sameKey.AddStateValue(apiKeyAddr, tfjsonpath.New("id")),
					newSecret.AddStateValue(apiKeyAddr, tfjsonpath.New("secret")),
				},
			},
			{
				Config:      apiKeyRotationConfig(name, "3"),
				ExpectError: regexp.MustCompile(`grace period`),
			},
		},
	}
}

func apiKeyBudget(tg target) resource.TestCase {
	name := sdkacctest.RandomWithPrefix("tf-acc")

	return resource.TestCase{
		PreCheck:                 tg.preCheck,
		ProtoV6ProviderFactories: tg.factories,
		CheckDestroy:             checkAPIKeysDestroyed(tg.client),
		Steps: []resource.TestStep{
			{
				Config: apiKeyConfig(name, "budget_cents = 500"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("budget_cents"), knownvalue.Int64Exact(500)),
				},
			},
			{
				Config: apiKeyConfig(name, "budget_cents = 1000"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("budget_cents"), knownvalue.Int64Exact(1000)),
				},
			},
			{
				Config: apiKeyConfig(name, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(apiKeyAddr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(apiKeyAddr, tfjsonpath.New("budget_cents"), knownvalue.Null()),
				},
			},
		},
	}
}

func apiKeyRotationConfig(name, version string) string {
	return apiKeyConfig(name, fmt.Sprintf(`keepers = { version = %q }
  rotation_grace_period_seconds = 3600`, version))
}

func apiKeyConfig(name, extra string) string {
	return fmt.Sprintf(`
resource "exa_api_key" "test" {
  name = %q
  %s
}
`, name, extra)
}

func checkAPIKeysDestroyed(client *exa.Client) func(*terraform.State) error {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != "exa_api_key" {
				continue
			}
			_, err := client.GetAPIKey(context.Background(), rs.Primary.ID)
			if err == nil {
				return fmt.Errorf("exa_api_key %s still exists", rs.Primary.ID)
			}
			if !exa.IsNotFound(err) {
				return err
			}
		}
		return nil
	}
}

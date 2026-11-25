# Terraform Provider for Exa

Manages [Exa](https://exa.ai) API keys through the [Team Management API](https://exa.ai/docs/reference/team-management/create-api-key).

```hcl
terraform {
  required_providers {
    exa = {
      source = "tryriot/exa"
    }
  }
}

provider "exa" {} # reads the team's service key from EXA_SERVICE_KEY

resource "exa_api_key" "search" {
  name       = "search-service"
  rate_limit = 10
}
```

`exa_api_key.search.secret` holds the key. It is stored in the Terraform state.

See [docs/](docs/) for every attribute.

## API behaviour the provider works around

- **Rate limits:** Exa can't remove a rate limit from an existing key, so removing `rate_limit` replaces the key, and the new key gets a new secret.
- **Budgets:** `budget_cents` needs the API key budgets feature on the Exa team. Without it, Exa answers 403 to any request that sends `budgetCents`, even `null`, so the provider sends `budgetCents` only when the config sets it or changes it.
- **Rotation:** changing `keepers` rotates the secret in place. Exa refuses another rotation while the previous secret is still in its grace period, so that apply fails until it expires.
- **Secrets on read:** every read returns the key's secret, in a field named `id`. As a result, `terraform import` restores `secret`, and a key rotated outside Terraform shows its new secret on the next refresh.

## Development

```sh
make build      # go build
make test       # unit tests, and every scenario against an in-memory fake of the API
make generate   # format examples and regenerate docs/ (needs terraform on PATH)
```

To try a local build, point Terraform at it in `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "tryriot/exa" = "/path/to/go/bin" # where `make install` puts terraform-provider-exa
  }
  direct {}
}
```

### Acceptance tests

```sh
EXA_SERVICE_KEY=<service key> make testacc
```

The same scenarios as `make test`, against the real API on the service key's team, so locally only, not in CI. They create their own `tf-acc-*` keys, check them, and delete them. They never list, change or delete any other key. `TestAccAPIKeyResource_budget` runs only with `EXA_ACC_BUDGETS=1`, on a team that has the budgets feature.

## Releasing

Push a `v*` tag. GoReleaser builds and signs the release with the `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets (see `.github/workflows/release.yml`).

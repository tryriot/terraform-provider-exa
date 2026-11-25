resource "exa_api_key" "search" {
  name       = "search-service"
  rate_limit = 10
}

resource "time_rotating" "crawler_key" {
  rotation_days = 90
}

resource "exa_api_key" "crawler" {
  name = "crawler"

  keepers = {
    rotated_at = time_rotating.crawler_key.id
  }
  rotation_grace_period_seconds = 86400
}

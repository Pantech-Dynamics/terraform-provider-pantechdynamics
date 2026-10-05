provider "pantechdynamics" {
  # Or set PANTECHDYNAMICS_BASE_URL and PANTECHDYNAMICS_API_KEY.
  base_url = "https://api.pantechdynamics.com/public/v1"
  api_key  = var.pantechdynamics_api_key

  # Optional. How long one API request may take. Defaults to 60s. Raise it if the
  # API is slow. Or set PANTECHDYNAMICS_REQUEST_TIMEOUT.
  # request_timeout = "90s"
}

package providers

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// ErrorGuidance describes repair steps without returning provider response bodies,
// URLs or credentials. Classification and retry policy remain independent.
func ErrorGuidance(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "Model request canceled."
	}
	status, _, _ := errorHTTPMetadata(err)
	text := strings.ToLower(err.Error())
	if (status == 400 || status == 422) &&
		(strings.Contains(text, "image") || strings.Contains(text, "vision")) &&
		(strings.Contains(text, "not support") || strings.Contains(text, "unsupported") || strings.Contains(text, "not allowed")) {
		return "The selected model does not support these image inputs. Select a vision-capable model or remove the images and send text."
	}
	failure := ClassifyError(err, "", "")
	if failure != nil {
		switch failure.Reason {
		case FailoverAuth:
			text := strings.ToLower(err.Error())
			if strings.Contains(text, "expired") || strings.Contains(text, "re-authenticate") ||
				strings.Contains(text, "token refresh failed") {
				return "Authentication failed: the saved login or token has expired. Re-authenticate the provider."
			}
			if strings.Contains(text, "no api key") || strings.Contains(text, "missing api key") ||
				strings.Contains(text, "no credentials") {
				return "Authentication failed: no API key is configured. Add an API key in this model's settings."
			}
			return "Authentication failed: check this model's API key, token, login and provider permissions."
		case FailoverBilling:
			return "Model quota or credit balance is exhausted. Check the provider's billing and usage limits."
		case FailoverRateLimit:
			return "The provider's rate limit was reached. Try again later or configure another model."
		case FailoverTimeout, FailoverNetwork:
			return "The model request could not complete. Try again later; check the provider's availability and network connection."
		case FailoverFormat:
			return "The provider rejected the request. Check the model, api_base, supported tools and request settings."
		}
	}
	if status == 404 {
		return "The model or endpoint was not found. Check the configured model identifier and api_base."
	}
	if status > 0 {
		return "The provider rejected the request. Check the model, api_base and provider settings."
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return "The model endpoint could not be reached. Check api_base, DNS, proxy and TLS certificate settings."
	}
	return ""
}

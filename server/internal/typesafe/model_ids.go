package typesafe

import (
	"regexp"
	"strings"
)

var openRouterJevAlias = regexp.MustCompile(`^jev-[0-9]+\.[0-9]+$`)
var openRouterReleaseDate = regexp.MustCompile(`^[0-9]{8}$`)

func openRouterModelID(model string) string {
	model = strings.TrimPrefix(model, "~")
	return strings.TrimPrefix(model, "typesafe/")
}

func isFloatingDecisionModel(model string, openRouter bool) bool {
	if openRouter {
		model = openRouterModelID(model)
	}
	return model == "jev-latest" || model == "jev-preview"
}

func decisionModelsMatch(requested, served string, openRouter, allowAlias bool) bool {
	if !openRouter {
		return requested == served
	}
	requested, served = openRouterModelID(requested), openRouterModelID(served)
	if requested == served {
		return true
	}
	// OpenRouter resolves a minor-version alias to a dated release. Concrete
	// releases and an explicit ExpectedModel retain exact version checks.
	if allowAlias && openRouterJevAlias.MatchString(requested) {
		date, ok := strings.CutPrefix(served, requested+"-")
		return ok && openRouterReleaseDate.MatchString(date)
	}
	return false
}

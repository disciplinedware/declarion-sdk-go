package platform

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/disciplinedware/declarion-sdk-go/tracing"
)

func tracedHTTPClient(client *http.Client, baseURL string) *http.Client {
	base, _ := url.Parse(baseURL)
	lookup := func(destination *url.URL) string {
		path := destination.EscapedPath()
		if base != nil {
			path = strings.TrimPrefix(path, strings.TrimRight(base.EscapedPath(), "/"))
		}
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) < 2 || parts[0] != "api" {
			return ""
		}
		switch {
		case len(parts) == 3 && parts[1] == "data":
			return "/api/data/{entity}"
		case len(parts) == 4 && parts[1] == "data":
			return "/api/data/{entity}/{id}"
		case len(parts) == 3 && parts[1] == "actions":
			return "/api/actions/{code}"
		case len(parts) == 3 && parts[1] == "params":
			return "/api/params/{code}"
		case len(parts) == 4 && parts[1] == "schema" && parts[2] == "entities":
			return "/api/schema/entities/{entity}"
		case len(parts) == 2 && parts[1] == "mcp":
			return "/api/mcp"
		default:
			return ""
		}
	}
	return tracing.HTTPClient(client, baseURL, tracing.WithURLTemplateLookup(lookup))
}

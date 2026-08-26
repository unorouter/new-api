package router

import (
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/go-fuego/fuego/extra/fuegogin"
	"github.com/go-fuego/fuego/option"
)

func scalarUIHandler(specURL string) http.Handler {
	page := `<!doctype html>
<html>
<head><title>API Reference</title><meta charset="utf-8"/><meta name="viewport" content="width=device-width,initial-scale=1"/></head>
<body>
<script id="api-reference" data-url="` + specURL + `" data-configuration='{"theme":"kepler","darkMode":true}'></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
}

// cleanupSpec strips fuego's auto-injected "#### Controller:" / "#### Middlewares:"
// description blocks. It also rounds-trips the spec through JSON to rewrite any
// "#/components/schemas/<bad-key>" refs that kin-openapi3gen emits for
// anonymous nested types (these break codegen tools like Orval).
func cleanupSpec(spec *openapi3.T) {
	const marker = "---\n\n"
	for _, pathItem := range spec.Paths.Map() {
		for _, op := range pathItem.Operations() {
			if idx := strings.Index(op.Description, marker); idx != -1 {
				op.Description = strings.TrimSpace(op.Description[idx+len(marker):])
			}
		}
	}

	// Find component keys that violate the OpenAPI spec
	// (https://spec.openapis.org/oas/v3.0.3.html#components-object).
	bad := []string{}
	for key := range spec.Components.Schemas {
		if !validSchemaKey.MatchString(key) {
			bad = append(bad, key)
		}
	}
	if len(bad) == 0 {
		return
	}

	// Rename in-place: drop the bad component, register the body under a valid
	// synthetic key, and rewrite every ref. Refs nested deep inside other
	// schemas are rewritten via JSON round-trip since openapi3.SchemaRefs are
	// pointers shared across the spec.
	raw, err := common.Marshal(spec)
	if err != nil {
		return
	}
	for i, key := range bad {
		newKey := fmt.Sprintf("AnonymousSchema%d", i)
		raw = bytes.ReplaceAll(raw,
			[]byte(`"#/components/schemas/`+key+`"`),
			[]byte(`"#/components/schemas/`+newKey+`"`))
	}
	if err := common.Unmarshal(raw, spec); err != nil {
		return
	}
	// Refs are rewritten; now rename the component keys themselves.
	for i, key := range bad {
		newKey := fmt.Sprintf("AnonymousSchema%d", i)
		spec.Components.Schemas[newKey] = spec.Components.Schemas[key]
		delete(spec.Components.Schemas, key)
	}

	// kin-openapi3gen sets nullable=true on the schema body of any struct that
	// was reached via a pointer (e.g. *Channel). When ExportComponentSchemas is
	// on, the body becomes the component definition, so the component itself
	// gets polluted with nullable=true. That generates broken TypeScript like
	// `interface Channel { ... } | null`. Clear nullable on every component.
	for _, ref := range spec.Components.Schemas {
		if ref != nil && ref.Value != nil {
			ref.Value.Nullable = false
		}
	}
}

// validSchemaKey matches the OpenAPI 3 component-key pattern.
// See https://spec.openapis.org/oas/v3.0.3.html#components-object
var validSchemaKey = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// newOpenAPIEngine creates the fuego engine with OpenAPI config and security schemes.
func newOpenAPIEngine() *fuego.Engine {
	engine := fuego.NewEngine(
		fuego.WithOpenAPIConfig(fuego.OpenAPIConfig{
			PrettyFormatJSON: true,
			SpecURL:          "/openapi.json",
			SwaggerURL:       "/swagger",
			UIHandler:        scalarUIHandler,
			Info: &openapi3.Info{
				Title:       "New API",
				Description: " ",
				Version:     common.Version,
			},
		}),
	)

	// Add auth schemes BEFORE route registration (fuego validates scheme refs at registration time)
	engine.OpenAPI.Description().Components.SecuritySchemes = openapi3.SecuritySchemes{
		"bearerAuth": &openapi3.SecuritySchemeRef{
			Value: openapi3.NewSecurityScheme().
				WithType("http").
				WithScheme("bearer").
				WithDescription("API token (sk-...). Used by relay routes (/v1/*) and token-based query endpoints."),
		},
		"accessToken": &openapi3.SecuritySchemeRef{
			Value: openapi3.NewSecurityScheme().
				WithType("apiKey").
				WithIn("header").
				WithName("Authorization").
				WithDescription("System access token for dashboard API routes. Must be paired with New-Api-User header."),
		},
		"newApiUser": &openapi3.SecuritySchemeRef{
			Value: openapi3.NewSecurityScheme().
				WithType("apiKey").
				WithIn("header").
				WithName("New-Api-User").
				WithDescription("User ID header. Required together with the access token or session cookie for dashboard API routes."),
		},
		"sessionAuth": &openapi3.SecuritySchemeRef{
			Value: openapi3.NewSecurityScheme().
				WithType("apiKey").
				WithIn("cookie").
				WithName("session").
				WithDescription("Session cookie from /api/user/login. Used by dashboard routes as an alternative to access token."),
		},
		"anthropicApiKey": &openapi3.SecuritySchemeRef{
			Value: openapi3.NewSecurityScheme().
				WithType("apiKey").
				WithIn("header").
				WithName("x-api-key").
				WithDescription("Anthropic-style API key. Used by /v1/messages and /v1/models."),
		},
	}

	return engine
}

// registerOpenAPIRoutes finalizes the spec and registers the JSON + Scalar UI routes.
// No-op when engine is nil (i.e. ENABLE_OPENAPI is not set).
func registerOpenAPIRoutes(engine *fuego.Engine, router *gin.Engine) {
	if engine == nil {
		return
	}
	spec := engine.OpenAPI.Description()
	cleanupSpec(spec)
	engine.RegisterOpenAPIRoutes(&fuegogin.OpenAPIHandler{GinEngine: router})
}

// ---- Security options ----
// Reusable OpenAPI security options matching middleware auth levels.
// Public routes: use secPublic (no auth required)
// UserAuth/AdminAuth/RootAuth: use secDashboard (access token + user ID, or session + user ID)
// TokenAuth: use secToken (Bearer sk-...)
// TokenAuthReadOnly: use secToken
// TokenOrUserAuth: use secTokenOrDashboard

var dashboardSecurity = option.Security(
	openapi3.SecurityRequirement{"accessToken": {}, "newApiUser": {}},
	openapi3.SecurityRequirement{"sessionAuth": {}, "newApiUser": {}},
)

var tokenSecurity = option.Security(
	openapi3.SecurityRequirement{"bearerAuth": {}},
)

var tokenOrDashboardSecurity = option.Security(
	openapi3.SecurityRequirement{"bearerAuth": {}},
	openapi3.SecurityRequirement{"accessToken": {}, "newApiUser": {}},
	openapi3.SecurityRequirement{"sessionAuth": {}, "newApiUser": {}},
)

var publicSecurity = option.Security(
	openapi3.SecurityRequirement{},
)

// secPublic marks a route as requiring no authentication.
func secPublic() func(*fuego.BaseRoute) { return publicSecurity }

// secDashboard marks a route as requiring dashboard auth (access token or session + user ID).
func secDashboard() func(*fuego.BaseRoute) { return dashboardSecurity }

// secToken marks a route as requiring a Bearer API token.
func secToken() func(*fuego.BaseRoute) { return tokenSecurity }

// secTokenOrDashboard marks a route as accepting either Bearer token or dashboard auth.
func secTokenOrDashboard() func(*fuego.BaseRoute) { return tokenOrDashboardSecurity }

package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Target families the user can pick in the config editor.
const (
	TargetPrometheus      = "prometheus"
	TargetVictoriaMetrics = "victoriametrics"
	TargetVictoriaLogs    = "victorialogs"
)

// Authentication modes against the remote (destination) Grafana.
const (
	AuthToken = "token" // service account token / API key (Bearer)
	AuthBasic = "basic" // username + password
	AuthNone  = "none"  // anonymous access on the remote Grafana
)

// targetPluginTypes maps a target family to the plugin type IDs that are
// accepted for the remote datasource. VictoriaMetrics is frequently exposed
// through the core Prometheus datasource, so that is accepted too.
var targetPluginTypes = map[string][]string{
	TargetPrometheus:      {"prometheus"},
	TargetVictoriaMetrics: {"victoriametrics-metrics-datasource", "victoriametrics-datasource", "prometheus"},
	TargetVictoriaLogs:    {"victoriametrics-logs-datasource", "victorialogs-datasource"},
}

// isTypeAllowed reports whether a remote plugin type belongs to the family.
func isTypeAllowed(family, pluginType string) bool {
	for _, t := range targetPluginTypes[family] {
		if t == pluginType {
			return true
		}
	}
	return false
}

// isLogsFamily reports whether the family talks the VictoriaLogs (LogsQL) API.
func isLogsFamily(family string) bool {
	return family == TargetVictoriaLogs
}

// Settings is the parsed datasource configuration.
type Settings struct {
	GrafanaURL           string `json:"grafanaUrl"`
	AuthType             string `json:"authType"`
	BasicAuthUsername    string `json:"basicAuthUsername"`
	TargetType           string `json:"targetType"`
	TargetDatasourceName string `json:"targetDatasourceName"`
	TargetDatasourceUID  string `json:"targetDatasourceUid"`

	// OrgID is the organization on the remote Grafana (X-Grafana-Org-Id).
	// Parsed separately because provisioning may supply it as a string.
	OrgID int64 `json:"-"`

	apiToken string
	password string
}

// LoadSettings parses and validates the instance settings.
func LoadSettings(s backend.DataSourceInstanceSettings) (Settings, error) {
	var cfg Settings
	if len(s.JSONData) > 0 {
		if err := json.Unmarshal(s.JSONData, &cfg); err != nil {
			return cfg, errors.New("invalid datasource configuration")
		}
		var raw map[string]any
		if err := json.Unmarshal(s.JSONData, &raw); err == nil {
			cfg.OrgID = parseInt64(raw["orgId"])
		}
	}

	cfg.GrafanaURL = strings.TrimRight(strings.TrimSpace(cfg.GrafanaURL), "/")
	cfg.TargetDatasourceName = strings.TrimSpace(cfg.TargetDatasourceName)
	cfg.TargetDatasourceUID = strings.TrimSpace(cfg.TargetDatasourceUID)
	if cfg.AuthType == "" {
		cfg.AuthType = AuthToken
	}
	if cfg.TargetType == "" {
		cfg.TargetType = TargetPrometheus
	}
	cfg.apiToken = s.DecryptedSecureJSONData["apiToken"]
	cfg.password = s.DecryptedSecureJSONData["basicAuthPassword"]

	return cfg, nil
}

// Validate returns a user-facing error describing the first problem found.
func (c Settings) Validate() error {
	if c.GrafanaURL == "" {
		return errors.New("remote Grafana URL is required")
	}
	u, err := url.Parse(c.GrafanaURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("remote Grafana URL must be an absolute http(s) URL")
	}
	if _, ok := targetPluginTypes[c.TargetType]; !ok {
		return fmt.Errorf("unsupported target type %q", c.TargetType)
	}
	if c.TargetDatasourceName == "" && c.TargetDatasourceUID == "" {
		return errors.New("target datasource name is required")
	}
	switch c.AuthType {
	case AuthToken:
		if c.apiToken == "" {
			return errors.New("service account token is required")
		}
	case AuthBasic:
		if c.BasicAuthUsername == "" || c.password == "" {
			return errors.New("username and password are required for basic authentication")
		}
	case AuthNone:
	default:
		return fmt.Errorf("unsupported authentication type %q", c.AuthType)
	}
	return nil
}

func parseInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
}

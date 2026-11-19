# Grafana2Grafana Proxy datasource

Let **Grafana X** query a **Prometheus**, **VictoriaMetrics** or **VictoriaLogs** datasource that lives on
**Grafana Y**, without giving Grafana X network access to the databases themselves.

```
Grafana X ──(panel / alert / Explore)──▶ Grafana2Grafana Proxy (backend)
                                              │  HTTPS + service account token
                                              ▼
                                         Grafana Y  /api/ds/query
                                              │
                                              ▼
                              Prometheus / VictoriaMetrics / VictoriaLogs
```

The plugin forwards each query to Grafana Y's `/api/ds/query` with the datasource reference swapped for the
target datasource. Grafana Y executes the query with its own datasource plugin, so results look exactly like
they would on Grafana Y. That includes time series, tables, heatmaps, log lines and stats. Because queries run
in the plugin backend, **alerting**, recorded queries and public dashboards on Grafana X work too.

Metadata calls (metric names, label values, log field names) are forwarded to Grafana Y's datasource resource
API. A data-proxy fallback handles datasources that don't expose that API. Only read-only metadata paths are
allowed.

## Configuration

| Setting | Description |
|---|---|
| **URL** | Base URL of Grafana Y, e.g. `https://grafana-y.example.com` |
| **Organization ID** | Optional. Sent as `X-Grafana-Org-Id`. Service account tokens are already bound to one org. |
| **Timeout** | HTTP timeout in seconds (default 30). |
| **Authentication** | *Service account token* (recommended), *Basic auth* (username/password) or *No auth* (anonymous Grafana Y). |
| **Skip TLS verify / CA cert** | TLS options for reaching Grafana Y. |
| **Destination → Type** | `Prometheus`, `VictoriaMetrics` or `VictoriaLogs`. Selects the query editor and is checked against the remote datasource type. |
| **Destination → Datasource name** | Exact name of the datasource on Grafana Y. Type it in, or save and click **Load from remote** to pick from a list. |

Accepted remote plugin types per destination type:

| Type | Remote datasource plugin IDs |
|---|---|
| Prometheus | `prometheus` |
| VictoriaMetrics | `victoriametrics-metrics-datasource`, `victoriametrics-datasource`, `prometheus` (Prometheus DS pointed at VM) |
| VictoriaLogs | `victoriametrics-logs-datasource`, `victorialogs-datasource` |

### Permissions on Grafana Y

Create a service account on Grafana Y (*Administration → Users and access → Service accounts*) with the
**Viewer** role and add a token. Viewer can query datasources. If the account can't read
`/api/datasources/name/:name`, the plugin falls back to `/api/frontend/settings` to resolve the name to a UID.
With Grafana Enterprise/Cloud datasource permissions, grant the account **Query** on the target datasource.

### Provisioning example

```yaml
apiVersion: 1
datasources:
  - name: Prometheus @ grafana-y
    type: tobiasworkstech-grafana2grafanaproxy-datasource
    uid: g2g-prom
    jsonData:
      grafanaUrl: https://grafana-y.example.com
      authType: token          # token | basic | none
      targetType: prometheus   # prometheus | victoriametrics | victorialogs
      targetDatasourceName: Prometheus
      # orgId: 1
      # basicAuthUsername: viewer      # when authType: basic
      # tlsSkipVerify: true
    secureJsonData:
      apiToken: ${GRAFANA_Y_TOKEN}
      # basicAuthPassword: ...         # when authType: basic
```

## Querying

**Prometheus / VictoriaMetrics.** Write PromQL or MetricsQL. Options: legend, format (time series/table/heatmap),
range/instant/both, and min step. `$__rate_interval`, `$__interval` and dashboard variables work; Grafana Y
expands the built-ins.

**VictoriaLogs.** Write LogsQL. Query types: *Raw logs*, *Stats*, *Stats range* and *Hits*, with line limit,
legend and step options.

### Variable queries

| Destination | Syntax |
|---|---|
| Prometheus / VM | `label_names()`, `label_values(label)`, `label_values(metric_or_selector, label)`, `metrics(regex)`, `query_result(expr)` |
| VictoriaLogs | `field_names()`, `field_names(logsql)`, `field_values(field)`, `field_values(field, logsql)` |

## Development

```bash
npm install
npm run build            # frontend
mage -v                  # backend, all platforms (go install github.com/magefile/mage@latest)
go test ./pkg/...
npm run typecheck && npm run lint

# Two-Grafana dev stack: grafana-x (:3000) → grafana-y (:3001) → Prometheus / VictoriaMetrics / VictoriaLogs
GOOS=linux GOARCH=arm64 go build -o dist/gpx_grafana2grafanaproxy_datasource_linux_arm64 ./pkg
npm run server
```

### Validating the proxy against direct queries

`npm run bench` runs a k6 load test (`scripts/k6/proxy-vs-direct.js`, needs [k6](https://grafana.com/docs/k6/latest/set-up/install-k6/)) against the dev stack. Every iteration sends the same query to Grafana Y's own datasource and through the proxy on Grafana X, checks that both return identical data, and records the latency the proxy adds. Thresholds fail the run on any mismatch or error. Point it at other instances with `DIRECT_URL`, `PROXY_URL`, `GRAFANA_USER`, `GRAFANA_PASSWORD` and `MAX_VUS`.

## License

Apache-2.0

# Changelog

## 0.1.1

- Faster query responses: responses from the remote Grafana are decoded in a single pass, roughly halving decode time for large metric and log results.
- Add a k6 test (`npm run bench`) that checks proxied results are identical to direct queries and measures the added latency.

## 0.1.0

- Initial release: query Prometheus, VictoriaMetrics and VictoriaLogs datasources that are configured on another Grafana instance.
- Service account token, basic auth or anonymous access to the remote Grafana; optional org ID, custom CA and TLS skip-verify.
- Destination type selector and datasource name (typed, or loaded from the remote).
- PromQL/MetricsQL and LogsQL query editors, template variable queries, alerting support.

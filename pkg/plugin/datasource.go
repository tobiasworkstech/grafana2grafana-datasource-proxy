package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/httpclient"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
	jsoniter "github.com/json-iterator/go"
)

// responseJSON decodes /api/ds/query responses. encoding/json scans the whole
// body before handing each result to the SDK's frame decoder; jsoniter
// streams it once, which roughly halves decode time on large responses.
var responseJSON = jsoniter.ConfigCompatibleWithStandardLibrary

// Make sure Datasource implements the required interfaces.
var (
	_ backend.QueryDataHandler      = (*Datasource)(nil)
	_ backend.CheckHealthHandler    = (*Datasource)(nil)
	_ backend.CallResourceHandler   = (*Datasource)(nil)
	_ instancemgmt.InstanceDisposer = (*Datasource)(nil)
)

// Read-only metadata endpoints that may be proxied to the remote datasource.
// Anything else (e.g. Prometheus admin APIs) is rejected.
var (
	metricsResourceRe = regexp.MustCompile(`^api/v1/(labels|label/[^/]+/values|series|metadata|query|query_range|query_exemplars|status/buildinfo)$`)
	logsResourceRe    = regexp.MustCompile(`^select/logsql/(field_names|field_values|facets|stream_field_names|stream_field_values|streams|hits|query|stats_query|stats_query_range)$`)
)

// Datasource forwards queries to a datasource on a remote Grafana.
type Datasource struct {
	cfg    Settings
	client *Client
}

// NewDatasource creates a new datasource instance.
func NewDatasource(ctx context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
	cfg, err := LoadSettings(s)
	if err != nil {
		return nil, err
	}

	// HTTPClientOptions picks up TLS (skip verify, custom CA, client cert)
	// and timeout from the standard jsonData/secureJsonData keys. Auth is
	// handled by Client, so make sure the SDK doesn't add its own.
	opts, err := s.HTTPClientOptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("http client options: %w", err)
	}
	opts.BasicAuth = nil
	opts.ForwardHTTPHeaders = false
	hc, err := httpclient.New(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	return &Datasource{cfg: cfg, client: NewClient(cfg, hc)}, nil
}

// Dispose is called when the settings change and the instance is replaced.
func (d *Datasource) Dispose() {}

// QueryData forwards every query to the remote Grafana's /api/ds/query.
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	resp := backend.NewQueryDataResponse()

	failAll := func(queries []backend.DataQuery, status backend.Status, msg string) {
		for _, q := range queries {
			resp.Responses[q.RefID] = backend.ErrDataResponseWithSource(status, backend.ErrorSourceDownstream, msg)
		}
	}

	if err := d.cfg.Validate(); err != nil {
		for _, q := range req.Queries {
			resp.Responses[q.RefID] = backend.ErrDataResponseWithSource(backend.StatusBadRequest, backend.ErrorSourcePlugin, err.Error())
		}
		return resp, nil
	}

	ds, err := d.client.Resolve(ctx, false)
	if err != nil {
		log.DefaultLogger.Error("Failed to resolve remote datasource", "error", err)
		failAll(req.Queries, backend.StatusBadGateway, userMessage(err))
		return resp, nil
	}

	// /api/ds/query takes a single time range per request, so group queries
	// that share one (time-shifted queries get their own request).
	type group struct {
		tr      backend.TimeRange
		queries []backend.DataQuery
	}
	var order []string
	groups := map[string]*group{}
	for _, q := range req.Queries {
		if isEmptyQuery(q) {
			resp.Responses[q.RefID] = backend.DataResponse{}
			continue
		}
		key := strconv.FormatInt(q.TimeRange.From.UnixMilli(), 10) + "-" + strconv.FormatInt(q.TimeRange.To.UnixMilli(), 10)
		g, ok := groups[key]
		if !ok {
			g = &group{tr: q.TimeRange}
			groups[key] = g
			order = append(order, key)
		}
		g.queries = append(g.queries, q)
	}

	for _, key := range order {
		g := groups[key]
		res, err := d.runGroup(ctx, ds, g.tr, g.queries)
		var re *remoteError
		if err != nil && errors.As(err, &re) && re.Status == http.StatusNotFound {
			// The remote datasource may have been recreated with a new UID.
			d.client.invalidate()
			if ds2, rerr := d.client.Resolve(ctx, true); rerr == nil {
				ds = ds2
				res, err = d.runGroup(ctx, ds, g.tr, g.queries)
			}
		}
		if err != nil {
			log.DefaultLogger.Error("Remote query failed", "error", err, "queries", len(g.queries))
			failAll(g.queries, backend.StatusBadGateway, userMessage(err))
			continue
		}
		for _, q := range g.queries {
			r, ok := res.Responses[q.RefID]
			if !ok {
				r = backend.ErrDataResponseWithSource(backend.StatusBadGateway, backend.ErrorSourceDownstream, "remote Grafana returned no result for this query")
			}
			resp.Responses[q.RefID] = r
		}
	}
	return resp, nil
}

func (d *Datasource) runGroup(ctx context.Context, ds RemoteDatasource, tr backend.TimeRange, queries []backend.DataQuery) (*backend.QueryDataResponse, error) {
	remoteQueries := make([]map[string]any, 0, len(queries))
	for _, q := range queries {
		remoteQueries = append(remoteQueries, buildRemoteQuery(q, ds))
	}
	payload := map[string]any{
		"from":    strconv.FormatInt(tr.From.UnixMilli(), 10),
		"to":      strconv.FormatInt(tr.To.UnixMilli(), 10),
		"queries": remoteQueries,
	}
	log.DefaultLogger.Debug("Forwarding queries to remote Grafana", "queries", len(queries), "remoteType", ds.Type)

	_, body, err := d.client.postQuery(ctx, payload)
	if err != nil {
		return nil, err
	}
	var out backend.QueryDataResponse
	if err := responseJSON.Unmarshal(body, &out); err != nil {
		return nil, &userError{msg: "could not decode the response from the remote Grafana"}
	}
	if out.Responses == nil {
		out.Responses = backend.Responses{}
	}
	return &out, nil
}

// buildRemoteQuery turns a local query into the JSON the remote Grafana
// expects: the stored query model (expr, legendFormat, queryType, ...) is
// kept as-is so the remote datasource plugin interprets it natively, and
// only the datasource reference and timing hints are replaced.
func buildRemoteQuery(q backend.DataQuery, ds RemoteDatasource) map[string]any {
	m := map[string]any{}
	if len(q.JSON) > 0 {
		_ = json.Unmarshal(q.JSON, &m)
	}
	delete(m, "datasource")
	delete(m, "datasourceId")
	delete(m, "hide")
	m["refId"] = q.RefID
	m["datasource"] = map[string]string{"uid": ds.UID, "type": ds.Type}
	if q.Interval > 0 {
		m["intervalMs"] = q.Interval.Milliseconds()
	}
	if q.MaxDataPoints > 0 {
		m["maxDataPoints"] = q.MaxDataPoints
	}
	if q.QueryType != "" {
		if _, ok := m["queryType"]; !ok {
			m["queryType"] = q.QueryType
		}
	}
	return m
}

func isEmptyQuery(q backend.DataQuery) bool {
	var m struct {
		Expr *string `json:"expr"`
	}
	if err := json.Unmarshal(q.JSON, &m); err != nil || m.Expr == nil {
		return false // unknown shape: let the remote decide
	}
	return strings.TrimSpace(*m.Expr) == ""
}

// CheckHealth verifies credentials, that the target datasource exists with
// the right type, and asks the remote Grafana to health-check it.
func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	if err := d.cfg.Validate(); err != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: err.Error()}, nil
	}
	ds, err := d.client.Resolve(ctx, true)
	if err != nil {
		log.DefaultLogger.Error("Health check: resolve failed", "error", err)
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: userMessage(err)}, nil
	}

	ok, msg, err := d.client.remoteHealth(ctx, ds.UID)
	if err != nil {
		log.DefaultLogger.Error("Health check: remote health call failed", "error", err)
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: userMessage(err)}, nil
	}
	target := fmt.Sprintf("%q (%s, uid %s)", ds.Name, ds.Type, ds.UID)
	if !ok {
		if msg == "" {
			msg = "remote health check failed"
		}
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: fmt.Sprintf("Connected to remote Grafana and found %s, but its health check failed: %s", target, msg),
		}, nil
	}
	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: fmt.Sprintf("Connected to remote Grafana. Target datasource %s is healthy.", target),
	}, nil
}

// CallResource serves:
//
//	GET  datasources        - remote datasources matching the target type
//	GET/POST remote/<path>  - read-only metadata calls forwarded to the
//	                          remote datasource (labels, field names, ...)
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	path := strings.Trim(req.Path, "/")

	switch {
	case path == "datasources":
		if req.Method != http.MethodGet {
			return sendJSON(sender, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		}
		if d.cfg.GrafanaURL == "" {
			return sendJSON(sender, http.StatusBadRequest, map[string]string{"message": "remote Grafana URL is not configured"})
		}
		list, err := d.client.ListDatasources(ctx)
		if err != nil {
			log.DefaultLogger.Error("List remote datasources failed", "error", err)
			return sendJSON(sender, http.StatusBadGateway, map[string]string{"message": userMessage(err)})
		}
		return sendJSON(sender, http.StatusOK, list)

	case strings.HasPrefix(path, "remote/"):
		return d.proxyRemote(ctx, strings.TrimPrefix(path, "remote/"), req, sender)
	}
	return sendJSON(sender, http.StatusNotFound, map[string]string{"message": "not found"})
}

func (d *Datasource) proxyRemote(ctx context.Context, rest string, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return sendJSON(sender, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
	allowed := metricsResourceRe
	if isLogsFamily(d.cfg.TargetType) {
		allowed = logsResourceRe
	}
	if strings.Contains(rest, "..") || !allowed.MatchString(rest) {
		return sendJSON(sender, http.StatusForbidden, map[string]string{"message": "path not allowed"})
	}
	if err := d.cfg.Validate(); err != nil {
		return sendJSON(sender, http.StatusBadRequest, map[string]string{"message": err.Error()})
	}
	ds, err := d.client.Resolve(ctx, false)
	if err != nil {
		return sendJSON(sender, http.StatusBadGateway, map[string]string{"message": userMessage(err)})
	}

	var rawQuery string
	if u, perr := url.Parse(req.URL); perr == nil {
		rawQuery = u.RawQuery
	}
	status, hdr, body, err := d.client.proxyResource(ctx, ds.UID, req.Method, rest, rawQuery, req.Body, isLogsFamily(d.cfg.TargetType))
	if err != nil {
		var re *remoteError
		if errors.As(err, &re) {
			return sendJSON(sender, re.Status, map[string]string{"message": userMessage(err)})
		}
		log.DefaultLogger.Error("Remote resource call failed", "error", err)
		return sendJSON(sender, http.StatusBadGateway, map[string]string{"message": userMessage(err)})
	}
	headers := map[string][]string{"Content-Type": {"application/json"}}
	if ct := hdr.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = []string{ct}
	}
	return sender.Send(&backend.CallResourceResponse{Status: status, Headers: headers, Body: body})
}

func sendJSON(sender backend.CallResourceResponseSender, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sender.Send(&backend.CallResourceResponse{
		Status:  status,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    b,
	})
}

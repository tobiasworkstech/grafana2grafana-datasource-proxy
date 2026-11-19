package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// fakeGrafana emulates the bits of the Grafana HTTP API the plugin uses.
type fakeGrafana struct {
	t           *testing.T
	dsType      string
	viewerOnly  bool // /api/datasources/* returns 403 → frontend settings fallback
	lastQuery   map[string]any
	queryCalls  atomic.Int32
	lastAuth    string
	lastOrg     string
	lastResPath string
}

func (f *fakeGrafana) handler() http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.lastAuth = r.Header.Get("Authorization")
			f.lastOrg = r.Header.Get("X-Grafana-Org-Id")
			if f.lastAuth != "Bearer good-token" {
				writeJSON(w, 401, map[string]string{"message": "invalid API key"})
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/api/datasources/name/", auth(func(w http.ResponseWriter, r *http.Request) {
		if f.viewerOnly {
			writeJSON(w, 403, map[string]string{"message": "forbidden"})
			return
		}
		if strings.TrimPrefix(r.URL.Path, "/api/datasources/name/") != "Remote Prom" {
			writeJSON(w, 404, map[string]string{"message": "Data source not found"})
			return
		}
		writeJSON(w, 200, RemoteDatasource{UID: "abc123", Name: "Remote Prom", Type: f.dsType})
	}))
	mux.HandleFunc("/api/datasources", auth(func(w http.ResponseWriter, r *http.Request) {
		if f.viewerOnly {
			writeJSON(w, 403, map[string]string{"message": "forbidden"})
			return
		}
		writeJSON(w, 200, []RemoteDatasource{{UID: "abc123", Name: "Remote Prom", Type: f.dsType}, {UID: "logs1", Name: "Logs", Type: "victoriametrics-logs-datasource"}})
	}))
	mux.HandleFunc("/api/frontend/settings", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"datasources": map[string]any{
			"Remote Prom": map[string]string{"uid": "abc123", "type": f.dsType},
			"Logs":        map[string]string{"uid": "logs1", "type": "victoriametrics-logs-datasource"},
		}})
	}))
	mux.HandleFunc("/api/datasources/uid/abc123/health", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "OK", "message": "Successfully queried the Prometheus API."})
	}))
	mux.HandleFunc("/api/datasources/uid/abc123/resources/", auth(func(w http.ResponseWriter, r *http.Request) {
		f.lastResPath = r.URL.Path + "?" + r.URL.RawQuery
		writeJSON(w, 200, map[string]any{"status": "success", "data": []string{"__name__", "job"}})
	}))
	mux.HandleFunc("/api/ds/query", auth(func(w http.ResponseWriter, r *http.Request) {
		f.queryCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.lastQuery = map[string]any{}
		_ = json.Unmarshal(b, &f.lastQuery)
		writeJSON(w, 200, json.RawMessage(`{"results":{"A":{"status":200,"frames":[{"schema":{"refId":"A","fields":[{"name":"Time","type":"time","typeInfo":{"frame":"time.Time"}},{"name":"Value","type":"number","typeInfo":{"frame":"float64"},"labels":{"job":"node"}}]},"data":{"values":[[1700000000000,1700000015000],[1,2]]}}]}}}`))
	}))
	return mux
}

func newTestDatasource(t *testing.T, f *fakeGrafana, jsonData map[string]any, secure map[string]string) *Datasource {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	jsonData["grafanaUrl"] = srv.URL + "/"
	b, _ := json.Marshal(jsonData)
	inst, err := NewDatasource(context.Background(), backend.DataSourceInstanceSettings{
		Type:                    "tobiasworkstech-grafana2grafanaproxy-datasource",
		JSONData:                b,
		DecryptedSecureJSONData: secure,
	})
	if err != nil {
		t.Fatalf("NewDatasource: %v", err)
	}
	return inst.(*Datasource)
}

func baseConfig() map[string]any {
	return map[string]any{
		"authType":             "token",
		"targetType":           "prometheus",
		"targetDatasourceName": "Remote Prom",
		"orgId":                "2",
	}
}

func TestQueryDataForwardsToRemote(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "good-token"})

	from := time.UnixMilli(1700000000000)
	to := time.UnixMilli(1700003600000)
	resp, err := d.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{
			RefID:         "A",
			JSON:          []byte(`{"refId":"A","expr":"up","legendFormat":"{{job}}","datasource":{"uid":"local","type":"tobiasworkstech-grafana2grafanaproxy-datasource"}}`),
			TimeRange:     backend.TimeRange{From: from, To: to},
			Interval:      15 * time.Second,
			MaxDataPoints: 500,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := resp.Responses["A"]
	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}
	if len(r.Frames) != 1 || r.Frames[0].Fields[1].Len() != 2 {
		t.Fatalf("unexpected frames: %+v", r.Frames)
	}
	if f.lastOrg != "2" {
		t.Errorf("org header = %q, want 2", f.lastOrg)
	}
	if f.lastQuery["from"] != "1700000000000" || f.lastQuery["to"] != "1700003600000" {
		t.Errorf("bad time range: %v %v", f.lastQuery["from"], f.lastQuery["to"])
	}
	q := f.lastQuery["queries"].([]any)[0].(map[string]any)
	ds := q["datasource"].(map[string]any)
	if ds["uid"] != "abc123" || ds["type"] != "prometheus" {
		t.Errorf("datasource not rewritten: %v", ds)
	}
	if q["expr"] != "up" || q["legendFormat"] != "{{job}}" {
		t.Errorf("query model not preserved: %v", q)
	}
	if q["intervalMs"] != float64(15000) || q["maxDataPoints"] != float64(500) {
		t.Errorf("timing hints missing: %v", q)
	}
}

func TestQueryDataSkipsEmptyExpr(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "good-token"})
	resp, _ := d.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: []byte(`{"expr":"  "}`)}},
	})
	if resp.Responses["A"].Error != nil || f.queryCalls.Load() != 0 {
		t.Fatalf("empty query should not be forwarded")
	}
}

func TestBadCredentials(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "wrong"})
	res, _ := d.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, "authentication") {
		t.Fatalf("got %v %q", res.Status, res.Message)
	}
}

func TestHealthOKAndViewerFallback(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus", viewerOnly: true}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "good-token"})
	res, _ := d.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if res.Status != backend.HealthStatusOk || !strings.Contains(res.Message, "abc123") {
		t.Fatalf("got %v %q", res.Status, res.Message)
	}
}

func TestTypeMismatch(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	cfg := baseConfig()
	cfg["targetType"] = "victorialogs"
	d := newTestDatasource(t, f, cfg, map[string]string{"apiToken": "good-token"})
	res, _ := d.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
	if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, "does not match") {
		t.Fatalf("got %v %q", res.Status, res.Message)
	}
}

func TestVictoriaMetricsAcceptsPrometheusType(t *testing.T) {
	if !isTypeAllowed(TargetVictoriaMetrics, "prometheus") || !isTypeAllowed(TargetVictoriaMetrics, "victoriametrics-metrics-datasource") {
		t.Fatal("victoriametrics family should accept prometheus and VM plugin types")
	}
	if isTypeAllowed(TargetPrometheus, "victoriametrics-logs-datasource") {
		t.Fatal("prometheus family must not accept logs")
	}
}

type captureSender struct{ resp *backend.CallResourceResponse }

func (c *captureSender) Send(r *backend.CallResourceResponse) error { c.resp = r; return nil }

func TestCallResourceProxyAndAllowlist(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "good-token"})

	s := &captureSender{}
	_ = d.CallResource(context.Background(), &backend.CallResourceRequest{
		Path: "remote/api/v1/labels", Method: "GET", URL: "remote/api/v1/labels?start=1&end=2",
	}, s)
	if s.resp.Status != 200 || !strings.Contains(string(s.resp.Body), "job") {
		t.Fatalf("proxy failed: %d %s", s.resp.Status, s.resp.Body)
	}
	if f.lastResPath != "/api/datasources/uid/abc123/resources/api/v1/labels?start=1&end=2" {
		t.Errorf("unexpected remote path %q", f.lastResPath)
	}

	for _, p := range []string{"remote/api/v1/admin/tsdb/delete_series", "remote/api/v1/../../admin", "remote/select/logsql/query"} {
		s = &captureSender{}
		_ = d.CallResource(context.Background(), &backend.CallResourceRequest{Path: p, Method: "POST", URL: p}, s)
		if s.resp.Status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", p, s.resp.Status)
		}
	}
}

func TestListDatasourcesFiltersByType(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus", viewerOnly: true}
	cfg := baseConfig()
	cfg["targetType"] = "victorialogs"
	d := newTestDatasource(t, f, cfg, map[string]string{"apiToken": "good-token"})
	s := &captureSender{}
	_ = d.CallResource(context.Background(), &backend.CallResourceRequest{Path: "datasources", Method: "GET"}, s)
	var list []RemoteDatasource
	_ = json.Unmarshal(s.resp.Body, &list)
	if s.resp.Status != 200 || len(list) != 1 || list[0].Name != "Logs" {
		t.Fatalf("got %d %s", s.resp.Status, s.resp.Body)
	}
}

func TestListDatasourcesAdmin(t *testing.T) {
	f := &fakeGrafana{t: t, dsType: "prometheus"}
	d := newTestDatasource(t, f, baseConfig(), map[string]string{"apiToken": "good-token"})
	s := &captureSender{}
	_ = d.CallResource(context.Background(), &backend.CallResourceRequest{Path: "datasources", Method: "GET"}, s)
	var list []RemoteDatasource
	_ = json.Unmarshal(s.resp.Body, &list)
	if s.resp.Status != 200 || len(list) != 1 || list[0].UID != "abc123" {
		t.Fatalf("got %d %s", s.resp.Status, s.resp.Body)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		cfg  Settings
		want string
	}{
		{Settings{}, "URL is required"},
		{Settings{GrafanaURL: "ftp://x", TargetType: "prometheus"}, "absolute http"},
		{Settings{GrafanaURL: "http://x", TargetType: "influx"}, "unsupported target"},
		{Settings{GrafanaURL: "http://x", TargetType: "prometheus"}, "name is required"},
		{Settings{GrafanaURL: "http://x", TargetType: "prometheus", TargetDatasourceName: "a", AuthType: AuthToken}, "token is required"},
		{Settings{GrafanaURL: "http://x", TargetType: "prometheus", TargetDatasourceName: "a", AuthType: AuthBasic}, "username and password"},
	}
	for _, c := range cases {
		err := c.cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestLogsResourceIsSentAsJSONPost(t *testing.T) {
	var gotMethod, gotCT string
	var gotBody map[string]string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/datasources/name/Logs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(RemoteDatasource{UID: "logs1", Name: "Logs", Type: "victoriametrics-logs-datasource"})
	})
	mux.HandleFunc("/api/datasources/uid/logs1/resources/select/logsql/field_values", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotCT = r.Method, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"values":[{"value":"info","hits":3}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	b, _ := json.Marshal(map[string]any{"grafanaUrl": srv.URL, "authType": "none", "targetType": "victorialogs", "targetDatasourceName": "Logs"})
	inst, err := NewDatasource(context.Background(), backend.DataSourceInstanceSettings{Type: "x", JSONData: b})
	if err != nil {
		t.Fatal(err)
	}
	s := &captureSender{}
	_ = inst.(*Datasource).CallResource(context.Background(), &backend.CallResourceRequest{
		Path: "remote/select/logsql/field_values", Method: "GET",
		URL: "remote/select/logsql/field_values?field=level&query=%2A&start=1&end=2",
	}, s)
	if s.resp.Status != 200 || gotMethod != "POST" || gotCT != "application/json" {
		t.Fatalf("status=%d method=%s ct=%s", s.resp.Status, gotMethod, gotCT)
	}
	if gotBody["field"] != "level" || gotBody["query"] != "*" || gotBody["start"] != "1" || gotBody["end"] != "2" {
		t.Fatalf("body = %v", gotBody)
	}
}

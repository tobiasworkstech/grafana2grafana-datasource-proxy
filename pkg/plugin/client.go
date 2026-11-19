package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// maxResponseBytes bounds how much of a remote response is read into memory.
const maxResponseBytes = 256 << 20 // 256 MiB

// resolveTTL is how long a resolved name→UID mapping is cached.
const resolveTTL = time.Minute

// RemoteDatasource identifies a datasource on the remote Grafana.
type RemoteDatasource struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// remoteError is returned for non-2xx responses from the remote Grafana.
type remoteError struct {
	Status  int
	Message string
}

func (e *remoteError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("remote Grafana returned %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("remote Grafana returned %d", e.Status)
}

// userMessage turns an error from the remote into something safe and useful
// to show in the UI (no internal addresses or stack details).
func userMessage(err error) string {
	var re *remoteError
	if errors.As(err, &re) {
		switch re.Status {
		case http.StatusUnauthorized:
			return "authentication to the remote Grafana failed - check the token or username/password"
		case http.StatusForbidden:
			return "the remote Grafana denied access - the account needs at least Viewer (query) permission on the target datasource"
		case http.StatusNotFound:
			return "not found on the remote Grafana"
		}
		if re.Message != "" {
			return re.Message
		}
		return fmt.Sprintf("remote Grafana returned HTTP %d", re.Status)
	}
	var ue *userError
	if errors.As(err, &ue) {
		return ue.msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request to the remote Grafana timed out"
	}
	return "could not reach the remote Grafana - check the URL and network connectivity"
}

// userError carries a message that is safe to show to the user verbatim.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

// Client talks to the remote (destination) Grafana HTTP API.
type Client struct {
	cfg  Settings
	http *http.Client

	mu         sync.Mutex
	resolved   *RemoteDatasource
	resolvedAt time.Time
}

// NewClient creates a client for the remote Grafana.
func NewClient(cfg Settings, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader) (*http.Request, error) {
	u := c.cfg.GrafanaURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	switch c.cfg.AuthType {
	case AuthToken:
		req.Header.Set("Authorization", "Bearer "+c.cfg.apiToken)
	case AuthBasic:
		req.SetBasicAuth(c.cfg.BasicAuthUsername, c.cfg.password)
	}
	if c.cfg.OrgID > 0 {
		req.Header.Set("X-Grafana-Org-Id", strconv.FormatInt(c.cfg.OrgID, 10))
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do performs a request and returns status and body; non-2xx (except
// 207 multi-status) are returned as *remoteError.
func (c *Client) do(req *http.Request) (int, http.Header, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, resp.Header, body, &remoteError{Status: resp.StatusCode, Message: extractMessage(body)}
	}
	return resp.StatusCode, resp.Header, body, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	_, _, body, err := c.do(req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &userError{msg: "unexpected response from the remote Grafana - is the URL pointing at a Grafana server?"}
	}
	return nil
}

// extractMessage pulls Grafana's {"message": "..."} out of an error body.
func extractMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &m) == nil {
		if m.Message != "" {
			return m.Message
		}
		return m.Error
	}
	return ""
}

// frontendSettings is the subset of /api/frontend/settings we need. This
// endpoint is available to any signed-in user (including Viewer service
// accounts), unlike /api/datasources which needs datasources:read.
type frontendSettings struct {
	Datasources map[string]RemoteDatasource `json:"datasources"`
}

// ListDatasources lists remote datasources matching the configured target
// family, sorted by name.
func (c *Client) ListDatasources(ctx context.Context) ([]RemoteDatasource, error) {
	all, err := c.allDatasources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteDatasource, 0, len(all))
	for _, ds := range all {
		if isTypeAllowed(c.cfg.TargetType, ds.Type) {
			out = append(out, ds)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Client) allDatasources(ctx context.Context) ([]RemoteDatasource, error) {
	var list []RemoteDatasource
	err := c.getJSON(ctx, "/api/datasources", &list)
	if err == nil {
		return list, nil
	}
	var re *remoteError
	if !errors.As(err, &re) || re.Status != http.StatusForbidden {
		return nil, err
	}
	// Viewer accounts can't list datasources; fall back to frontend settings.
	var fs frontendSettings
	if err := c.getJSON(ctx, "/api/frontend/settings", &fs); err != nil {
		return nil, err
	}
	for name, ds := range fs.Datasources {
		if ds.Name == "" {
			ds.Name = name
		}
		list = append(list, ds)
	}
	return list, nil
}

// Resolve finds the configured target datasource on the remote Grafana and
// verifies its type. Results are cached for resolveTTL.
func (c *Client) Resolve(ctx context.Context, force bool) (RemoteDatasource, error) {
	c.mu.Lock()
	if !force && c.resolved != nil && time.Since(c.resolvedAt) < resolveTTL {
		ds := *c.resolved
		c.mu.Unlock()
		return ds, nil
	}
	c.mu.Unlock()

	ds, err := c.lookup(ctx)
	if err != nil {
		return ds, err
	}
	if !isTypeAllowed(c.cfg.TargetType, ds.Type) {
		return ds, &userError{msg: fmt.Sprintf("remote datasource %q is of type %q, which does not match the selected target type %q", ds.Name, ds.Type, c.cfg.TargetType)}
	}

	c.mu.Lock()
	c.resolved = &ds
	c.resolvedAt = time.Now()
	c.mu.Unlock()
	return ds, nil
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.resolved = nil
	c.mu.Unlock()
}

func (c *Client) lookup(ctx context.Context) (RemoteDatasource, error) {
	var ds RemoteDatasource
	var path string
	if c.cfg.TargetDatasourceName != "" {
		path = "/api/datasources/name/" + url.PathEscape(c.cfg.TargetDatasourceName)
	} else {
		path = "/api/datasources/uid/" + url.PathEscape(c.cfg.TargetDatasourceUID)
	}
	err := c.getJSON(ctx, path, &ds)
	if err == nil && ds.UID != "" {
		return ds, nil
	}
	var re *remoteError
	if err != nil && (!errors.As(err, &re) || (re.Status != http.StatusForbidden && re.Status != http.StatusNotFound)) {
		return ds, err
	}

	// Fallback that works for Viewer-role accounts.
	var fs frontendSettings
	if err := c.getJSON(ctx, "/api/frontend/settings", &fs); err != nil {
		return ds, err
	}
	for name, cand := range fs.Datasources {
		if cand.Name == "" {
			cand.Name = name
		}
		if (c.cfg.TargetDatasourceName != "" && cand.Name == c.cfg.TargetDatasourceName) ||
			(c.cfg.TargetDatasourceName == "" && cand.UID == c.cfg.TargetDatasourceUID) {
			return cand, nil
		}
	}
	target := c.cfg.TargetDatasourceName
	if target == "" {
		target = c.cfg.TargetDatasourceUID
	}
	return ds, &userError{msg: fmt.Sprintf("datasource %q was not found on the remote Grafana (or the account cannot see it)", target)}
}

// remoteHealth calls the remote datasource health check. It returns
// (ok, message, err); err is set only on transport/auth problems.
func (c *Client) remoteHealth(ctx context.Context, uid string) (bool, string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/datasources/uid/"+url.PathEscape(uid)+"/health", nil, nil)
	if err != nil {
		return false, "", err
	}
	_, _, body, err := c.do(req)
	var m struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	if err != nil {
		var re *remoteError
		if errors.As(err, &re) && re.Status != http.StatusUnauthorized {
			if m.Message == "" {
				m.Message = re.Message
			}
			return false, m.Message, nil
		}
		return false, "", err
	}
	return true, m.Message, nil
}

// postQuery sends a /api/ds/query request and returns the raw response body
// along with the HTTP status. 4xx responses that carry per-query results
// (Grafana answers 400/207 with a results map) are not treated as errors.
func (c *Client) postQuery(ctx context.Context, payload any) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/ds/query", nil, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	status, _, body, err := c.do(req)
	if err != nil {
		var re *remoteError
		if errors.As(err, &re) && hasResults(body) {
			return status, body, nil
		}
		return status, nil, err
	}
	return status, body, nil
}

func hasResults(body []byte) bool {
	var probe struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	return json.Unmarshal(body, &probe) == nil && len(probe.Results) > 0
}

// proxyResource forwards a metadata call (labels, metric names, field
// names ...) to the remote datasource. It first uses the datasource
// resource API, which works for backend plugins (Prometheus,
// VictoriaMetrics, VictoriaLogs), and falls back to the classic data
// proxy if the resource route does not exist.
//
// The VictoriaLogs plugin's resource API does not read URL parameters; it
// expects a POST with a JSON object of string values ({"query": "*",
// "field": "level", ...}). For logs targets the query string is translated
// into that shape for the resource route, while the data-proxy fallback
// (which talks to VictoriaLogs directly) keeps the plain query string.
func (c *Client) proxyResource(ctx context.Context, uid, method, rest, rawQuery string, body []byte, logs bool) (int, http.Header, []byte, error) {
	type attempt struct {
		method, path, rawQuery, contentType string
		body                                []byte
	}
	resourcePath := "/api/datasources/uid/" + url.PathEscape(uid) + "/resources/" + rest
	proxyPath := "/api/datasources/proxy/uid/" + url.PathEscape(uid) + "/" + rest

	form := "application/x-www-form-urlencoded"
	attempts := []attempt{
		{method, resourcePath, rawQuery, form, body},
		{method, proxyPath, rawQuery, form, body},
	}
	if logs {
		params, _ := url.ParseQuery(rawQuery)
		if len(body) > 0 {
			if extra, err := url.ParseQuery(string(body)); err == nil {
				for k, v := range extra {
					params[k] = v
				}
			}
		}
		obj := map[string]string{}
		for k, v := range params {
			if len(v) > 0 {
				obj[k] = v[0]
			}
		}
		jb, _ := json.Marshal(obj)
		attempts[0] = attempt{http.MethodPost, resourcePath, "", "application/json", jb}
		attempts[1] = attempt{http.MethodGet, proxyPath, params.Encode(), "", nil}
	}

	var (
		status int
		hdr    http.Header
		resp   []byte
		err    error
	)
	for _, a := range attempts {
		p := a.path
		if a.rawQuery != "" {
			p += "?" + a.rawQuery
		}
		var rdr io.Reader
		if len(a.body) > 0 {
			rdr = bytes.NewReader(a.body)
		}
		req, rerr := c.newRequest(ctx, a.method, p, nil, rdr)
		if rerr != nil {
			return 0, nil, nil, rerr
		}
		if len(a.body) > 0 {
			req.Header.Set("Content-Type", a.contentType)
		}
		status, hdr, resp, err = c.do(req)
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			return status, hdr, resp, err
		}
	}
	return status, hdr, resp, err
}

// k6 validation: the same query sent directly to Grafana-Y's datasource and through the
// Grafana2Grafana proxy on Grafana-X. Every iteration checks that both answers are
// identical and records how much time the proxy adds.
//
//   npm run server                        # dev stack: grafana-x :3000, grafana-y :3001
//   k6 run scripts/k6/proxy-vs-direct.js
//
// Env overrides: DIRECT_URL, PROXY_URL, GRAFANA_USER, GRAFANA_PASSWORD, MAX_VUS
import http from 'k6/http';
import encoding from 'k6/encoding';
import { check, fail } from 'k6';
import { Trend, Rate, Counter } from 'k6/metrics';
import { textSummary } from 'https://jslib.k6.io/k6-summary/0.1.0/index.js';

const DIRECT_URL = __ENV.DIRECT_URL || 'http://localhost:3001';
const PROXY_URL = __ENV.PROXY_URL || 'http://localhost:3000';
const AUTH = 'Basic ' + encoding.b64encode(`${__ENV.GRAFANA_USER || 'admin'}:${__ENV.GRAFANA_PASSWORD || 'admin'}`);
const MAX_VUS = parseInt(__ENV.MAX_VUS || '40', 10);

// [name, direct datasource uid on grafana-y, proxy datasource uid on grafana-x, query]
const CASES = [
  [
    'prom_range',
    'y-prom',
    'g2g-prom',
    {
      expr: 'rate(prometheus_http_requests_total[1m])',
      range: true,
      instant: false,
      intervalMs: 15000,
      maxDataPoints: 1000,
    },
  ],
  [
    'vm_range',
    'y-vm',
    'g2g-vm',
    { expr: 'rate(vm_http_requests_total[1m])', range: true, instant: false, intervalMs: 15000, maxDataPoints: 1000 },
  ],
  // VictoriaLogs only orders log lines by _time; lines sharing a timestamp have no defined
  // order, so this case compares rows as a set (strict order is tracked in row_order_identical).
  ['vl_logs', 'y-vl', 'g2g-vl', { expr: 'app:demo', queryType: 'instant', maxLines: 1000 }, { unorderedRows: true }],
  ['vl_stats', 'y-vl', 'g2g-vl', { expr: 'app:demo | stats by (level) count()', queryType: 'stats' }],
];

const latency = new Trend('query_latency', true); // tags: path=direct|proxy, case
const overhead = new Trend('proxy_overhead', true); // proxy minus direct, same iteration
const identical = new Rate('results_identical');
const orderIdentical = new Rate('row_order_identical'); // informational: also requires identical row order
const mismatches = new Counter('result_mismatches');

export const options = {
  scenarios: {
    ramp: {
      executor: 'ramping-vus',
      startVUs: 1,
      stages: [
        { duration: '20s', target: Math.ceil(MAX_VUS / 8) },
        { duration: '40s', target: Math.ceil(MAX_VUS / 2) },
        { duration: '40s', target: MAX_VUS },
        { duration: '10s', target: 0 },
      ],
    },
  },
  thresholds: {
    results_identical: ['rate>0.999'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    'query_latency{path:proxy}': ['p(95)<500'],
    proxy_overhead: ['med<50', 'p(95)<200'],
  },
  summaryTrendStats: ['med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};
// Per-case sub-metrics only appear in the summary when a threshold names them.
for (const [name, , , , opts = {}] of CASES) {
  for (const path of ['direct', 'proxy']) {
    options.thresholds[`query_latency{case:${name},path:${path}}`] = ['max>=0'];
  }
  options.thresholds[`proxy_overhead{case:${name}}`] = ['max>-1e9'];
  options.thresholds[`results_identical{case:${name}}`] = ['rate>0.999'];
  if (opts.unorderedRows) {
    options.thresholds[`row_order_identical{case:${name}}`] = ['rate>=0'];
  }
}

// Fixed, minute-aligned window ending a minute in the past, so the data cannot change
// during the run and both paths must return exactly the same result.
export function setup() {
  const to = Math.floor(Date.now() / 60000) * 60000 - 60000;
  const range = { from: String(to - 15 * 60000), to: String(to) };
  for (const [name, yuid, xuid, q] of CASES) {
    for (const [base, uid] of [
      [DIRECT_URL, yuid],
      [PROXY_URL, xuid],
    ]) {
      const r = query(base, uid, q, range, name, 'warmup');
      if (r.status !== 200) {
        fail(`${name} on ${base} returned ${r.status}: ${r.body}`);
      }
    }
  }
  return { range };
}

function query(base, uid, q, range, name, path) {
  const body = JSON.stringify({ ...range, queries: [{ ...q, refId: 'A', datasource: { uid } }] });
  return http.post(`${base}/api/ds/query`, body, {
    headers: { 'Content-Type': 'application/json', Authorization: AUTH },
    tags: { case: name, path, name: `${path}:${name}` },
  });
}

// JSON with object keys sorted at every level. The proxy re-encodes responses in Go, which
// sorts map keys, while the direct response keeps the backend's key order (e.g. VictoriaLogs
// log-line fields), so a plain JSON.stringify would report false mismatches.
function canonical(v) {
  if (Array.isArray(v)) {
    return '[' + v.map(canonical).join(',') + ']';
  }
  if (v && typeof v === 'object') {
    return (
      '{' +
      Object.keys(v)
        .sort()
        .map((k) => JSON.stringify(k) + ':' + canonical(v[k]))
        .join(',') +
      '}'
    );
  }
  return JSON.stringify(v);
}

// Field names, labels and values of every frame; ignores frame meta such as executed-query strings.
function signature(res, unorderedRows) {
  let parsed;
  try {
    parsed = res.json();
  } catch (e) {
    return null;
  }
  const out = [];
  for (const ref of Object.keys(parsed.results || {}).sort()) {
    const r = parsed.results[ref];
    if (r.error) {
      return null;
    }
    for (const f of r.frames || []) {
      const fields = f.schema.fields.map((fl) => [fl.name, fl.labels || {}]);
      let values = f.data.values;
      if (unorderedRows && values.length) {
        // transpose to rows, sort them, transpose back
        const rows = values[0].map((_, i) => canonical(values.map((col) => col[i]))).sort();
        values = rows;
      }
      out.push(canonical([fields, values]));
    }
  }
  return out.sort().join('\n');
}

export default function (data) {
  const [name, yuid, xuid, q, opts = {}] = CASES[(__VU + __ITER) % CASES.length];
  const legs = [
    ['direct', DIRECT_URL, yuid],
    ['proxy', PROXY_URL, xuid],
  ];
  if (Math.random() < 0.5) {
    legs.reverse();
  } // cancel ordering/cache bias

  const res = {};
  for (const [path, base, uid] of legs) {
    res[path] = query(base, uid, q, data.range, name, path);
    latency.add(res[path].timings.duration, { path, case: name });
  }

  const ok = check(res, {
    'direct 200': (r) => r.direct.status === 200,
    'proxy 200': (r) => r.proxy.status === 200,
  });
  if (!ok) {
    return;
  }

  const sd = signature(res.direct, opts.unorderedRows);
  const sp = signature(res.proxy, opts.unorderedRows);
  const same = sd !== null && sd === sp;
  if (opts.unorderedRows) {
    const strict = signature(res.direct);
    orderIdentical.add(strict !== null && strict === signature(res.proxy), { case: name });
  }
  identical.add(same, { case: name });
  check(null, { 'proxy result identical to direct': () => same });
  if (!same) {
    mismatches.add(1, { case: name });
    if (__ITER < 3) {
      let at = 0;
      while (sd && sp && at < sd.length && sd[at] === sp[at]) {
        at++;
      }
      console.warn(
        `${name}: mismatch at char ${at}: direct …${sd ? sd.slice(Math.max(0, at - 80), at + 80) : 'error'}… proxy …${
          sp ? sp.slice(Math.max(0, at - 80), at + 80) : 'error'
        }…`
      );
    }
  }
  overhead.add(res.proxy.timings.duration - res.direct.timings.duration, { case: name });
}

export function handleSummary(data) {
  const m = data.metrics;
  const v = (key, stat) => (m[key] && m[key].values[stat] !== undefined ? m[key].values[stat] : NaN);
  const lines = [
    '',
    'Per-case latency (ms)          direct med / p95      proxy med / p95     overhead med / p95   identical',
  ];
  for (const [name] of CASES) {
    const d = `query_latency{case:${name},path:direct}`;
    const p = `query_latency{case:${name},path:proxy}`;
    const o = `proxy_overhead{case:${name}}`;
    const i = `results_identical{case:${name}}`;
    const f = (x) => (isNaN(x) ? '   n/a' : x.toFixed(1).padStart(6));
    lines.push(
      `  ${name.padEnd(28)} ${f(v(d, 'med'))} / ${f(v(d, 'p(95)'))}     ${f(v(p, 'med'))} / ${f(v(p, 'p(95)'))}     ${f(
        v(o, 'med')
      )} / ${f(v(o, 'p(95)'))}   ${isNaN(v(i, 'rate')) ? 'n/a' : (v(i, 'rate') * 100).toFixed(2) + '%'}`
    );
  }
  return { stdout: textSummary(data, { indent: ' ', enableColors: true }) + lines.join('\n') + '\n\n' };
}

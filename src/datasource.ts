import {
  CoreApp,
  DataSourceInstanceSettings,
  MetricFindValue,
  ScopedVars,
  dateTime,
  LegacyMetricFindQueryOptions,
  TimeRange,
} from '@grafana/data';
import { DataSourceWithBackend, getTemplateSrv } from '@grafana/runtime';

import {
  DEFAULT_LOGS_QUERY,
  DEFAULT_METRICS_QUERY,
  G2GOptions,
  G2GQuery,
  TargetType,
  isLogsTarget,
} from './types';

/**
 * Frontend for the Grafana2Grafana proxy. Queries run through the plugin
 * backend (so alerting and public dashboards work); metadata calls go through
 * the backend resource endpoint `remote/<path>`, which forwards them to the
 * target datasource on the remote Grafana.
 */
export class DataSource extends DataSourceWithBackend<G2GQuery, G2GOptions> {
  targetType: TargetType;

  constructor(instanceSettings: DataSourceInstanceSettings<G2GOptions>) {
    super(instanceSettings);
    this.targetType = instanceSettings.jsonData.targetType ?? 'prometheus';
  }

  isLogs(): boolean {
    return isLogsTarget(this.targetType);
  }

  getDefaultQuery(_app: CoreApp): Partial<G2GQuery> {
    return this.isLogs() ? DEFAULT_LOGS_QUERY : DEFAULT_METRICS_QUERY;
  }

  filterQuery(query: G2GQuery): boolean {
    return !query.hide && !!query.expr?.trim();
  }

  applyTemplateVariables(query: G2GQuery, scopedVars: ScopedVars): G2GQuery {
    const tpl = getTemplateSrv();
    return {
      ...query,
      expr: tpl.replace(query.expr ?? '', scopedVars, this.isLogs() ? undefined : 'regex'),
      interval: query.interval ? tpl.replace(query.interval, scopedVars) : query.interval,
      step: query.step ? tpl.replace(query.step, scopedVars) : query.step,
    };
  }

  /** Metric names (Prometheus / VictoriaMetrics) for the query editor helper. */
  async getMetricNames(): Promise<string[]> {
    const res = await this.getResource('remote/api/v1/label/__name__/values');
    return res?.data ?? [];
  }

  /** Field names (VictoriaLogs) for the query editor helper. */
  async getLogFieldNames(range?: TimeRange): Promise<string[]> {
    const { start, end } = this.timeParams(range ? { range } : undefined);
    const res = await this.getResource('remote/select/logsql/field_names', { query: '*', start, end });
    return (res?.values ?? []).map((v: { value: string }) => v.value);
  }

  /**
   * Variable queries.
   *  Metrics: label_names() | label_values(label) | label_values(metric, label) | metrics(regex) | query_result(expr)
   *  Logs:    field_names() | field_values(field) | field_values(field, logsql)
   */
  async metricFindQuery(query: string, options?: LegacyMetricFindQueryOptions): Promise<MetricFindValue[]> {
    const q = getTemplateSrv().replace(query, options?.scopedVars).trim();
    if (!q) {
      return [];
    }
    return this.isLogs() ? this.logsFind(q, options) : this.metricsFind(q, options);
  }

  private timeParams(options?: LegacyMetricFindQueryOptions) {
    const range = options?.range;
    const to = range ? range.to.valueOf() : dateTime().valueOf();
    const from = range ? range.from.valueOf() : to - 3600 * 1000;
    return { start: Math.floor(from / 1000), end: Math.floor(to / 1000) };
  }

  private async metricsFind(q: string, options?: LegacyMetricFindQueryOptions): Promise<MetricFindValue[]> {
    const time = this.timeParams(options);
    const toText = (arr?: string[]) => (arr ?? []).map((text) => ({ text }));

    if (/^label_names\(\s*\)$/.test(q)) {
      const res = await this.getResource('remote/api/v1/labels', time);
      return toText(res?.data);
    }
    const lv = q.match(/^label_values\((?:(.+),\s*)?([a-zA-Z_][a-zA-Z0-9_]*)\s*\)$/);
    if (lv) {
      const params: Record<string, unknown> = { ...time };
      if (lv[1]) {
        params['match[]'] = lv[1].trim();
      }
      const res = await this.getResource(`remote/api/v1/label/${encodeURIComponent(lv[2])}/values`, params);
      return toText(res?.data);
    }
    const metrics = q.match(/^metrics\((.*)\)$/);
    if (metrics) {
      const res = await this.getResource('remote/api/v1/label/__name__/values', time);
      let names: string[] = res?.data ?? [];
      if (metrics[1].trim()) {
        const re = new RegExp(metrics[1].trim());
        names = names.filter((n) => re.test(n));
      }
      return toText(names);
    }
    const qr = q.match(/^query_result\((.+)\)$/);
    const expr = qr ? qr[1] : q;
    const res = await this.getResource('remote/api/v1/query', { query: expr, time: time.end });
    const result: Array<{ metric?: Record<string, string>; value?: [number, string] }> = res?.data?.result ?? [];
    return result.map((r) => {
      const { __name__, ...labels } = r.metric ?? {};
      const lbl = Object.entries(labels)
        .map(([k, v]) => `${k}="${v}"`)
        .join(', ');
      return { text: `${__name__ ?? ''}{${lbl}} ${r.value?.[1] ?? ''} ${r.value?.[0] ?? ''}`.trim() };
    });
  }

  private async logsFind(q: string, options?: LegacyMetricFindQueryOptions): Promise<MetricFindValue[]> {
    const time = this.timeParams(options);
    const values = (res: { values?: Array<{ value: string }> }) =>
      (res?.values ?? []).map((v) => ({ text: v.value }));

    const fn = q.match(/^field_names\((.*)\)$/);
    if (fn) {
      const res = await this.getResource('remote/select/logsql/field_names', { query: fn[1].trim() || '*', ...time });
      return values(res);
    }
    const fv = q.match(/^field_values\(\s*([^,\s)]+)\s*(?:,\s*(.+))?\)$/);
    if (fv) {
      const res = await this.getResource('remote/select/logsql/field_values', {
        field: fv[1],
        query: fv[2]?.trim() || '*',
        limit: 1000,
        ...time,
      });
      return values(res);
    }
    return [];
  }
}

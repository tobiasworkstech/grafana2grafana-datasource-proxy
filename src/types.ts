import { DataSourceJsonData, SelectableValue } from '@grafana/data';
import { DataQuery } from '@grafana/schema';

export type TargetType = 'prometheus' | 'victoriametrics' | 'victorialogs';
export type AuthType = 'token' | 'basic' | 'none';

export const TARGET_TYPE_OPTIONS: Array<SelectableValue<TargetType>> = [
  { label: 'Prometheus', value: 'prometheus', description: 'Core Prometheus datasource on the remote Grafana' },
  {
    label: 'VictoriaMetrics',
    value: 'victoriametrics',
    description: 'VictoriaMetrics plugin datasource (or a Prometheus datasource pointing at VictoriaMetrics)',
  },
  { label: 'VictoriaLogs', value: 'victorialogs', description: 'VictoriaLogs plugin datasource (LogsQL)' },
];

export const isLogsTarget = (t?: TargetType) => t === 'victorialogs';

/**
 * A query is stored in the shape the *remote* datasource understands, so it is
 * forwarded to the remote Grafana unchanged:
 *  - Prometheus / VictoriaMetrics: expr, legendFormat, format, range, instant, interval
 *  - VictoriaLogs: expr, queryType (instant|stats|statsRange|hits), legendFormat, maxLines, step
 */
export interface G2GQuery extends DataQuery {
  expr: string;
  legendFormat?: string;

  // metrics
  format?: 'time_series' | 'table' | 'heatmap';
  range?: boolean;
  instant?: boolean;
  interval?: string;

  // logs
  queryType?: 'instant' | 'stats' | 'statsRange' | 'hits';
  maxLines?: number;
  step?: string;
}

export const DEFAULT_METRICS_QUERY: Partial<G2GQuery> = { expr: '', range: true, instant: false, format: 'time_series' };
export const DEFAULT_LOGS_QUERY: Partial<G2GQuery> = { expr: '', queryType: 'instant', maxLines: 1000 };

export interface G2GOptions extends DataSourceJsonData {
  grafanaUrl?: string;
  orgId?: number;
  authType?: AuthType;
  basicAuthUsername?: string;
  targetType?: TargetType;
  targetDatasourceName?: string;
  targetDatasourceUid?: string;
  // read by the plugin SDK's HTTP client
  tlsSkipVerify?: boolean;
  tlsAuthWithCACert?: boolean;
  timeout?: number;
}

export interface G2GSecureOptions {
  apiToken?: string;
  basicAuthPassword?: string;
  tlsCACert?: string;
}

export interface RemoteDatasource {
  uid: string;
  name: string;
  type: string;
}

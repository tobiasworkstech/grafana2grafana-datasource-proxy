import React, { ChangeEvent, KeyboardEvent, useState } from 'react';
import { css } from '@emotion/css';
import { GrafanaTheme2, QueryEditorProps, SelectableValue } from '@grafana/data';
import {
  InlineField,
  InlineFieldRow,
  Input,
  RadioButtonGroup,
  Select,
  Stack,
  TextArea,
  useStyles2,
} from '@grafana/ui';

import { DataSource } from '../datasource';
import { G2GOptions, G2GQuery } from '../types';

type Props = QueryEditorProps<DataSource, G2GQuery, G2GOptions>;

const FORMAT_OPTIONS: Array<SelectableValue<G2GQuery['format']>> = [
  { label: 'Time series', value: 'time_series' },
  { label: 'Table', value: 'table' },
  { label: 'Heatmap', value: 'heatmap' },
];

type RangeMode = 'range' | 'instant' | 'both';
const RANGE_OPTIONS: Array<SelectableValue<RangeMode>> = [
  { label: 'Range', value: 'range' },
  { label: 'Instant', value: 'instant' },
  { label: 'Both', value: 'both' },
];

const LOGS_TYPE_OPTIONS: Array<SelectableValue<NonNullable<G2GQuery['queryType']>>> = [
  { label: 'Raw logs', value: 'instant', description: 'Log lines (/select/logsql/query)' },
  { label: 'Stats', value: 'stats', description: 'Instant stats (/select/logsql/stats_query)' },
  { label: 'Stats range', value: 'statsRange', description: 'Stats over time (/select/logsql/stats_query_range)' },
  { label: 'Hits', value: 'hits', description: 'Log volume (/select/logsql/hits)' },
];

const getStyles = (theme: GrafanaTheme2) => ({
  expr: css({
    fontFamily: theme.typography.fontFamilyMonospace,
    fontSize: theme.typography.bodySmall.fontSize,
    width: '100%',
  }),
  hint: css({
    color: theme.colors.text.secondary,
    fontSize: theme.typography.bodySmall.fontSize,
  }),
});

export function QueryEditor({ query, datasource, onChange, onRunQuery, range }: Props) {
  const styles = useStyles2(getStyles);
  const isLogs = datasource.isLogs();
  const [names, setNames] = useState<Array<SelectableValue<string>>>();
  const [namesLoading, setNamesLoading] = useState(false);

  const update = (patch: Partial<G2GQuery>, run = false) => {
    onChange({ ...query, ...patch });
    if (run) {
      onRunQuery();
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.shiftKey || e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      onRunQuery();
    }
  };

  const loadNames = async () => {
    if (names) {
      return;
    }
    setNamesLoading(true);
    try {
      const list = isLogs ? await datasource.getLogFieldNames(range) : await datasource.getMetricNames();
      setNames(list.map((n) => ({ label: n, value: n })));
    } catch {
      setNames([]);
    } finally {
      setNamesLoading(false);
    }
  };

  const insertName = (name?: string) => {
    if (!name) {
      return;
    }
    const expr = query.expr ?? '';
    if (isLogs) {
      update({ expr: expr.trim() ? `${expr} ${name}:*` : `${name}:*` });
    } else {
      update({ expr: expr.trim() ? `${expr}${name}` : name }, !expr.trim());
    }
  };

  const rangeMode: RangeMode =
    query.range && query.instant ? 'both' : query.instant ? 'instant' : 'range';

  return (
    <Stack direction="column" gap={1}>
      <InlineFieldRow>
        <InlineField
          label={isLogs ? 'Field' : 'Metric'}
          tooltip={isLogs ? 'Insert a field filter' : 'Insert a metric name'}
        >
          <Select
            width={32}
            options={names ?? []}
            isLoading={namesLoading}
            onOpenMenu={loadNames}
            placeholder={isLogs ? 'Field names' : 'Metrics browser'}
            value={null}
            onChange={(v) => insertName(v?.value)}
          />
        </InlineField>
        <span className={styles.hint}>
          Executed by the remote Grafana as {isLogs ? 'LogsQL' : 'PromQL / MetricsQL'} · Shift+Enter to run
        </span>
      </InlineFieldRow>

      <TextArea
        className={styles.expr}
        rows={3}
        value={query.expr ?? ''}
        placeholder={isLogs ? '_stream:{app="nginx"} error | limit 100' : 'rate(http_requests_total[$__rate_interval])'}
        onChange={(e: ChangeEvent<HTMLTextAreaElement>) => update({ expr: e.target.value })}
        onBlur={onRunQuery}
        onKeyDown={onKeyDown}
      />

      {isLogs ? (
        <InlineFieldRow>
          <InlineField label="Query type">
            <RadioButtonGroup
              options={LOGS_TYPE_OPTIONS}
              value={query.queryType ?? 'instant'}
              onChange={(v) => update({ queryType: v }, true)}
            />
          </InlineField>
          {(query.queryType ?? 'instant') === 'instant' ? (
            <InlineField label="Line limit" tooltip="Maximum number of log lines returned">
              <Input
                width={12}
                type="number"
                min={1}
                defaultValue={query.maxLines ?? 1000}
                onBlur={(e) => update({ maxLines: parseInt(e.currentTarget.value, 10) || undefined }, true)}
              />
            </InlineField>
          ) : (
            <>
              <InlineField label="Legend" tooltip="Series name, e.g. {{level}}">
                <Input
                  width={24}
                  defaultValue={query.legendFormat ?? ''}
                  placeholder="auto"
                  onBlur={(e) => update({ legendFormat: e.currentTarget.value }, true)}
                />
              </InlineField>
              <InlineField label="Step" tooltip="Bucket size for stats range / hits, e.g. 1m. Empty = auto">
                <Input
                  width={12}
                  defaultValue={query.step ?? ''}
                  placeholder="auto"
                  onBlur={(e) => update({ step: e.currentTarget.value }, true)}
                />
              </InlineField>
            </>
          )}
        </InlineFieldRow>
      ) : (
        <InlineFieldRow>
          <InlineField label="Legend" tooltip="Series name override, e.g. {{instance}}">
            <Input
              width={24}
              defaultValue={query.legendFormat ?? ''}
              placeholder="auto"
              onBlur={(e) => update({ legendFormat: e.currentTarget.value }, true)}
            />
          </InlineField>
          <InlineField label="Format">
            <Select
              width={16}
              options={FORMAT_OPTIONS}
              value={query.format ?? 'time_series'}
              onChange={(v) => update({ format: v.value }, true)}
            />
          </InlineField>
          <InlineField label="Type">
            <RadioButtonGroup
              options={RANGE_OPTIONS}
              value={rangeMode}
              onChange={(v) =>
                update({ range: v === 'range' || v === 'both', instant: v === 'instant' || v === 'both' }, true)
              }
            />
          </InlineField>
          <InlineField label="Min step" tooltip="Lower bound for the step, e.g. 30s. Empty = auto">
            <Input
              width={12}
              defaultValue={query.interval ?? ''}
              placeholder="auto"
              onBlur={(e) => update({ interval: e.currentTarget.value }, true)}
            />
          </InlineField>
        </InlineFieldRow>
      )}
    </Stack>
  );
}

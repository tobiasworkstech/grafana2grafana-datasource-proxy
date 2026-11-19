import React, { ChangeEvent, useState } from 'react';
import { css } from '@emotion/css';
import { DataSourcePluginOptionsEditorProps, GrafanaTheme2, SelectableValue } from '@grafana/data';
import { getBackendSrv } from '@grafana/runtime';
import {
  Alert,
  Button,
  FieldSet,
  InlineField,
  InlineSwitch,
  Input,
  RadioButtonGroup,
  SecretInput,
  SecretTextArea,
  Select,
  Stack,
  useStyles2,
} from '@grafana/ui';

import { AuthType, G2GOptions, G2GSecureOptions, RemoteDatasource, TARGET_TYPE_OPTIONS, TargetType } from '../types';

type Props = DataSourcePluginOptionsEditorProps<G2GOptions, G2GSecureOptions>;

const LABEL_WIDTH = 26;
const INPUT_WIDTH = 48;

const AUTH_OPTIONS: Array<SelectableValue<AuthType>> = [
  { label: 'Service account token', value: 'token' },
  { label: 'Basic auth', value: 'basic' },
  { label: 'No auth', value: 'none' },
];

const getStyles = (theme: GrafanaTheme2) => ({
  help: css({
    color: theme.colors.text.secondary,
    fontSize: theme.typography.bodySmall.fontSize,
    marginBottom: theme.spacing(2),
    maxWidth: theme.spacing(100),
  }),
  code: css({
    fontFamily: theme.typography.fontFamilyMonospace,
  }),
});

export function ConfigEditor({ options, onOptionsChange }: Props) {
  const styles = useStyles2(getStyles);
  const { jsonData, secureJsonFields, secureJsonData } = options;
  const authType: AuthType = jsonData.authType ?? 'token';
  const targetType: TargetType = jsonData.targetType ?? 'prometheus';

  const [remoteOptions, setRemoteOptions] = useState<Array<SelectableValue<string>>>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string>();

  const setJson = <K extends keyof G2GOptions>(key: K, value: G2GOptions[K]) =>
    onOptionsChange({ ...options, jsonData: { ...jsonData, [key]: value } });

  const setSecure = (key: keyof G2GSecureOptions, value: string) =>
    onOptionsChange({ ...options, secureJsonData: { ...secureJsonData, [key]: value } });

  const resetSecure = (key: keyof G2GSecureOptions) =>
    onOptionsChange({
      ...options,
      secureJsonFields: { ...secureJsonFields, [key]: false },
      secureJsonData: { ...secureJsonData, [key]: '' },
    });

  const loadRemoteDatasources = async () => {
    setLoading(true);
    setLoadError(undefined);
    try {
      const list = await getBackendSrv().get<RemoteDatasource[]>(
        `/api/datasources/uid/${options.uid}/resources/datasources`
      );
      setRemoteOptions(list.map((d) => ({ label: d.name, value: d.name, description: `${d.type} · uid ${d.uid}` })));
      if (list.length === 0) {
        setLoadError(`No ${targetType} datasources visible to this account on the remote Grafana.`);
      }
    } catch (e: unknown) {
      const err = e as { data?: { message?: string }; message?: string };
      setLoadError(err?.data?.message ?? err?.message ?? 'Failed to load datasources');
    } finally {
      setLoading(false);
    }
  };

  const nameOptions: Array<SelectableValue<string>> = [...remoteOptions];
  if (jsonData.targetDatasourceName && !nameOptions.some((o) => o.value === jsonData.targetDatasourceName)) {
    nameOptions.unshift({ label: jsonData.targetDatasourceName, value: jsonData.targetDatasourceName });
  }

  return (
    <>
      <div className={styles.help}>
        Queries made with this datasource are forwarded to a Prometheus, VictoriaMetrics or VictoriaLogs datasource that
        is configured on another Grafana (the <em>remote</em> Grafana). The remote Grafana executes the query with its
        own datasource and returns the result. Use a remote service account with the <strong>Viewer</strong> role
        (or query permission on the target datasource).
      </div>

      <FieldSet label="Remote Grafana">
        <InlineField label="URL" labelWidth={LABEL_WIDTH} tooltip="Base URL of the remote Grafana" required>
          <Input
            width={INPUT_WIDTH}
            value={jsonData.grafanaUrl ?? ''}
            placeholder="https://grafana-y.example.com"
            onChange={(e: ChangeEvent<HTMLInputElement>) => setJson('grafanaUrl', e.target.value)}
          />
        </InlineField>
        <InlineField
          label="Organization ID"
          labelWidth={LABEL_WIDTH}
          tooltip="Org on the remote Grafana (sent as X-Grafana-Org-Id). Leave empty for the account's default org. Service account tokens are already bound to one org."
        >
          <Input
            width={16}
            type="number"
            min={1}
            value={jsonData.orgId ?? ''}
            placeholder="default"
            onChange={(e: ChangeEvent<HTMLInputElement>) =>
              setJson('orgId', e.target.value ? parseInt(e.target.value, 10) : undefined)
            }
          />
        </InlineField>
        <InlineField label="Timeout (seconds)" labelWidth={LABEL_WIDTH} tooltip="HTTP timeout for remote requests">
          <Input
            width={16}
            type="number"
            min={1}
            value={jsonData.timeout ?? ''}
            placeholder="30"
            onChange={(e: ChangeEvent<HTMLInputElement>) =>
              setJson('timeout', e.target.value ? parseInt(e.target.value, 10) : undefined)
            }
          />
        </InlineField>
      </FieldSet>

      <FieldSet label="Credentials">
        <InlineField label="Authentication" labelWidth={LABEL_WIDTH}>
          <RadioButtonGroup options={AUTH_OPTIONS} value={authType} onChange={(v) => setJson('authType', v)} />
        </InlineField>

        {authType === 'token' && (
          <InlineField
            label="Service account token"
            labelWidth={LABEL_WIDTH}
            tooltip="Token of a service account on the remote Grafana (Administration → Users and access → Service accounts)"
            required
          >
            <SecretInput
              width={INPUT_WIDTH}
              isConfigured={!!secureJsonFields?.apiToken}
              value={secureJsonData?.apiToken ?? ''}
              placeholder="glsa_..."
              onReset={() => resetSecure('apiToken')}
              onChange={(e: ChangeEvent<HTMLInputElement>) => setSecure('apiToken', e.target.value)}
            />
          </InlineField>
        )}

        {authType === 'basic' && (
          <>
            <InlineField label="Username" labelWidth={LABEL_WIDTH} required>
              <Input
                width={INPUT_WIDTH}
                value={jsonData.basicAuthUsername ?? ''}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setJson('basicAuthUsername', e.target.value)}
              />
            </InlineField>
            <InlineField label="Password" labelWidth={LABEL_WIDTH} required>
              <SecretInput
                width={INPUT_WIDTH}
                isConfigured={!!secureJsonFields?.basicAuthPassword}
                value={secureJsonData?.basicAuthPassword ?? ''}
                onReset={() => resetSecure('basicAuthPassword')}
                onChange={(e: ChangeEvent<HTMLInputElement>) => setSecure('basicAuthPassword', e.target.value)}
              />
            </InlineField>
          </>
        )}

        <InlineField label="Skip TLS verify" labelWidth={LABEL_WIDTH}>
          <InlineSwitch
            value={!!jsonData.tlsSkipVerify}
            onChange={(e) => setJson('tlsSkipVerify', e.currentTarget.checked)}
          />
        </InlineField>
        <InlineField label="With CA cert" labelWidth={LABEL_WIDTH} tooltip="Trust a custom CA for the remote Grafana">
          <InlineSwitch
            value={!!jsonData.tlsAuthWithCACert}
            onChange={(e) => setJson('tlsAuthWithCACert', e.currentTarget.checked)}
          />
        </InlineField>
        {jsonData.tlsAuthWithCACert && (
          <InlineField label="CA certificate" labelWidth={LABEL_WIDTH}>
            <SecretTextArea
              cols={INPUT_WIDTH}
              rows={6}
              isConfigured={!!secureJsonFields?.tlsCACert}
              value={secureJsonData?.tlsCACert ?? ''}
              placeholder="-----BEGIN CERTIFICATE-----"
              onReset={() => resetSecure('tlsCACert')}
              onChange={(e: ChangeEvent<HTMLTextAreaElement>) => setSecure('tlsCACert', e.target.value)}
            />
          </InlineField>
        )}
      </FieldSet>

      <FieldSet label="Destination datasource">
        <InlineField
          label="Type"
          labelWidth={LABEL_WIDTH}
          tooltip="Kind of datasource on the remote Grafana. Determines the query editor (PromQL/MetricsQL or LogsQL)."
          required
        >
          <Select
            width={INPUT_WIDTH}
            options={TARGET_TYPE_OPTIONS}
            value={targetType}
            onChange={(v) => {
              setRemoteOptions([]);
              setJson('targetType', v.value ?? 'prometheus');
            }}
          />
        </InlineField>
        <InlineField
          label="Datasource name"
          labelWidth={LABEL_WIDTH}
          tooltip="Exact name of the datasource on the remote Grafana. Type it, or save first and then load the list from the remote."
          required
        >
          <Stack direction="row" gap={1}>
            <Select
              width={INPUT_WIDTH}
              options={nameOptions}
              value={jsonData.targetDatasourceName ?? null}
              allowCustomValue
              isClearable
              placeholder="e.g. Prometheus"
              onCreateOption={(v) => setJson('targetDatasourceName', v)}
              onChange={(v) => setJson('targetDatasourceName', v?.value ?? '')}
            />
            <Button
              variant="secondary"
              icon={loading ? 'spinner' : 'sync'}
              disabled={loading || !options.uid || !options.version}
              onClick={loadRemoteDatasources}
              tooltip="Lists remote datasources of the selected type. Uses the saved settings - click Save & test first."
            >
              Load from remote
            </Button>
          </Stack>
        </InlineField>
        {loadError && (
          <Alert severity="warning" title="Could not load remote datasources">
            {loadError}
          </Alert>
        )}
      </FieldSet>
    </>
  );
}

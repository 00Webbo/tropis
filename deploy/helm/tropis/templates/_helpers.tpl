{{/* Chart name. */}}
{{- define "tropis.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified release name. */}}
{{- define "tropis.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "tropis.labels" -}}
app.kubernetes.io/name: {{ include "tropis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Selector labels for the collector pods. */}}
{{- define "tropis.collectorSelector" -}}
app.kubernetes.io/name: {{ include "tropis.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: collector
{{- end -}}

{{/* The same selector as a label-selector string, for the analyser. */}}
{{- define "tropis.collectorSelectorString" -}}
app.kubernetes.io/name={{ include "tropis.name" . }},app.kubernetes.io/instance={{ .Release.Name }},app.kubernetes.io/component=collector
{{- end -}}

{{/* Image reference. */}}
{{- define "tropis.image" -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}

{{/* The analyser pod spec, shared by the CronJob, the install Job and the test. */}}
{{- define "tropis.analyzerPod" -}}
serviceAccountName: {{ include "tropis.fullname" .root }}-analyzer
{{- with .root.Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  runAsGroup: 65532
  seccompProfile: {type: RuntimeDefault}
containers:
  - name: tropis
    image: {{ include "tropis.image" .root }}
    imagePullPolicy: {{ .root.Values.image.pullPolicy }}
    args:
      {{- toYaml .args | nindent 6 }}
    env:
      - name: POD_NAMESPACE
        valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
      - name: NODE_NAME
        valueFrom: {fieldRef: {fieldPath: spec.nodeName}}
      - name: TROPIS_COLLECTOR_SELECTOR
        value: {{ include "tropis.collectorSelectorString" .root | quote }}
      - name: TROPIS_BACKEND
        value: {{ .root.Values.backend.provider | quote }}
      {{- with .root.Values.backend.model }}
      - name: TROPIS_MODEL
        value: {{ . | quote }}
      {{- end }}
      {{- with .root.Values.backend.baseURL }}
      - name: TROPIS_BASE_URL
        value: {{ . | quote }}
      {{- end }}
      - name: TROPIS_LOCAL_API
        value: {{ .root.Values.backend.localAPI | quote }}
      - name: TROPIS_CONTEXT_TOKENS
        value: {{ .root.Values.backend.contextTokens | quote }}
      {{- with .root.Values.backend.effort }}
      - name: TROPIS_EFFORT
        value: {{ . | quote }}
      {{- end }}
      {{- with .root.Values.backend.escalationModel }}
      - name: TROPIS_ESCALATION_MODEL
        value: {{ . | quote }}
      {{- end }}
      - name: TROPIS_NOTIFY_ON
        value: {{ .root.Values.notifications.on | quote }}
      - name: TROPIS_EVENTS
        value: {{ .root.Values.notifications.events.enabled | quote }}
      {{- with .root.Values.notifications.slack.webhookSecret }}
      {{- if .name }}
      - name: TROPIS_SLACK_WEBHOOK_URL
        valueFrom:
          secretKeyRef: {name: {{ .name }}, key: {{ .key }}}
      {{- end }}
      {{- end }}
      {{- with .root.Values.notifications.webhook.urlSecret }}
      {{- if .name }}
      - name: TROPIS_WEBHOOK_URL
        valueFrom:
          secretKeyRef: {name: {{ .name }}, key: {{ .key }}}
      {{- end }}
      {{- end }}
      {{- if .root.Values.backend.apiKeySecret.name }}
      - name: TROPIS_API_KEY
        valueFrom:
          secretKeyRef:
            name: {{ .root.Values.backend.apiKeySecret.name }}
            key: {{ .root.Values.backend.apiKeySecret.key }}
      {{- end }}
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities: {drop: [ALL]}
    resources:
      {{- toYaml .root.Values.sweep.resources | nindent 6 }}
{{- end -}}

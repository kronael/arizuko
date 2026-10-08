{{/*
Chart name (with override).
*/}}
{{- define "arizuko.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified release name.
*/}}
{{- define "arizuko.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Common labels.
*/}}
{{- define "arizuko.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "arizuko.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels for a single daemon. Call with (dict "ctx" . "daemon" "<name>").
*/}}
{{- define "arizuko.selectorLabels" -}}
app.kubernetes.io/name: {{ include "arizuko.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .daemon }}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "arizuko.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "arizuko.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
ConfigMap name.
*/}}
{{- define "arizuko.configMapName" -}}
{{- printf "%s-config" (include "arizuko.fullname" .) -}}
{{- end -}}

{{/*
PVC name for the data volume.
*/}}
{{- define "arizuko.pvcName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-data" (include "arizuko.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
In-cluster URL of a daemon's Service. Every daemon listens on :8080.
Call with (dict "ctx" . "daemon" "<name>").
*/}}
{{- define "arizuko.daemonURL" -}}
{{- printf "http://%s-%s:8080" (include "arizuko.fullname" .ctx) .daemon -}}
{{- end -}}

{{/*
Name of a daemon's own Secret: <fullname>-<daemon>, whether the chart renders it
or the operator does (secrets.external). Call with (dict "ctx" . "daemon" "<name>").
*/}}
{{- define "arizuko.daemonSecretName" -}}
{{- printf "%s-%s" (include "arizuko.fullname" .ctx) .daemon -}}
{{- end -}}

{{/*
A daemon's AUTHD_SERVICE_KEY from secrets.daemons.<daemon>.serviceKey, demanded.
Call with (dict "ctx" . "daemon" "<name>").
*/}}
{{- define "arizuko.serviceKey" -}}
{{- $msg := printf "secrets.daemons.%s.serviceKey must be set: %s exchanges it at authd for its service token and exits without one (openssl rand -hex 32; authd learns the set from these same values)" .daemon .daemon -}}
{{- required $msg (dig .daemon "serviceKey" "" .ctx.Values.secrets.daemons) -}}
{{- end -}}

{{/*
One half of a daemon's store credential from secrets.daemons.<daemon>, demanded.
Call with (dict "ctx" . "daemon" "<name>" "field" "storeKeyId"|"storeSecret").
*/}}
{{- define "arizuko.storeKey" -}}
{{- $msg := printf "secrets.daemons.%s.%s must be set: %s signs every store call with its own access key, minted by `extenddb init` (INSTALL.md § The store server)" .daemon .field .daemon -}}
{{- required $msg (dig .daemon .field "" .ctx.Values.secrets.daemons) -}}
{{- end -}}

{{/*
A daemon's Secret: its service key, its store credential when it reaches the
store (store.StoreReach), and the extra keys the caller demanded. Call with
(dict "ctx" . "daemon" "<name>" "store" <bool> "keys" <dict env -> value>).
*/}}
{{- define "arizuko.daemonSecret" -}}
apiVersion: v1
kind: Secret
metadata:
  name: {{ include "arizuko.daemonSecretName" . }}
  labels:
    {{- include "arizuko.labels" .ctx | nindent 4 }}
    app.kubernetes.io/component: {{ .daemon }}
type: Opaque
stringData:
  {{- if ne .daemon "authd" }}
  AUTHD_SERVICE_KEY: {{ include "arizuko.serviceKey" . | quote }}
  {{- end }}
  {{- if .store }}
  AWS_ACCESS_KEY_ID: {{ include "arizuko.storeKey" (dict "ctx" .ctx "daemon" .daemon "field" "storeKeyId") | quote }}
  AWS_SECRET_ACCESS_KEY: {{ include "arizuko.storeKey" (dict "ctx" .ctx "daemon" .daemon "field" "storeSecret") | quote }}
  {{- end }}
  {{- range $k, $v := .keys }}
  {{ $k }}: {{ $v | quote }}
  {{- end }}
{{- end -}}

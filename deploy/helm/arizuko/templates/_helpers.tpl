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
Secret name — the inline chart Secret, or an operator-supplied one.
*/}}
{{- define "arizuko.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "arizuko.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
ConfigMap name.
*/}}
{{- define "arizuko.configMapName" -}}
{{- printf "%s-config" (include "arizuko.fullname" .) -}}
{{- end -}}

{{/*
PVC name for the data dir.
*/}}
{{- define "arizuko.pvcName" -}}
{{- if .Values.persistence.existingClaim -}}
{{- .Values.persistence.existingClaim -}}
{{- else -}}
{{- printf "%s-data" (include "arizuko.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Service name for the canonical router (gated).
*/}}
{{- define "arizuko.routerService" -}}
{{- printf "%s-gated" (include "arizuko.fullname" .) -}}
{{- end -}}

{{/*
Router URL adapters/webd post inbound to.
*/}}
{{- define "arizuko.routerURL" -}}
{{- printf "http://%s:8080" (include "arizuko.routerService" .) -}}
{{- end -}}

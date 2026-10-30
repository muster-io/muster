{{- /*
SPDX-License-Identifier: AGPL-3.0-only
Copyright The Muster Authors
*/ -}}
{{/*
Expand the name of the chart.
*/}}
{{- define "muster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "muster.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "muster.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "muster.labels" -}}
helm.sh/chart: {{ include "muster.chart" . }}
{{ include "muster.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "muster.selectorLabels" -}}
app.kubernetes.io/name: {{ include "muster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "muster.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "muster.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The Secret with the master keys, the database password and the bootstrap Admin password; rendering fails without it.
*/}}
{{- define "muster.existingSecret" -}}
{{- required "the master keys are never generated: create a Secret and set existingSecret" .Values.existingSecret }}
{{- end }}

{{/*
The Secret with the database password or URL: database.existingSecret, such as the app Secret of CloudNativePG, or
else existingSecret.
*/}}
{{- define "muster.databaseSecret" -}}
{{- .Values.database.existingSecret | default (include "muster.existingSecret" .) }}
{{- end }}

{{/*
Return the image: <registry>/<repository>:<tag, or the appVersion>. global.imageRegistry wins over image.registry. An
image.repository with a "/" whose first path segment is a registry host (it holds a "." or ":", or is localhost) is used
as it is; one without a "/", such as muster, is a path under the registry.
*/}}
{{- define "muster.image" -}}
{{- $repository := .Values.image.repository -}}
{{- $first := first (splitList "/" $repository) -}}
{{- $registry := (.Values.global | default dict).imageRegistry | default .Values.image.registry -}}
{{- $hasHost := and (contains "/" $repository) (or (contains "." $first) (contains ":" $first) (eq $first "localhost")) -}}
{{- if or $hasHost (not $registry) -}}
{{- printf "%s:%s" $repository (default .Chart.AppVersion .Values.image.tag) }}
{{- else -}}
{{- printf "%s/%s:%s" (trimSuffix "/" $registry) $repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}

{{/*
The label matchers that select Muster's series in every chart rule: alerting.jobSelector, or the job the ServiceMonitor
scrapes into, named after the app.kubernetes.io/name label.
*/}}
{{- define "muster.jobSelector" -}}
{{- .Values.alerting.jobSelector | default (printf "job=%q" (include "muster.name" .)) }}
{{- end }}

{{/*
The version segment of the documentation site for the appVersion, as internalalerts.RunbookVersion computes it for
the binary (C-19.FR-9): major.minor of a release X.Y.Z, with an optional leading v, and latest for any other version.
*/}}
{{- define "muster.runbookVersion" -}}
{{- $v := toString .Chart.AppVersion }}
{{- $release := `^v?(\d+)\.(\d+)\.\d+$` }}
{{- if regexMatch $release $v }}
{{- regexReplaceAll $release $v "${1}.${2}" }}
{{- else }}
{{- "latest" }}
{{- end }}
{{- end }}

{{/*
The runbook pages of the documentation site for this appVersion; a rule's runbook_url is this, its name and a slash.
*/}}
{{- define "muster.runbookBase" -}}
{{- printf "%s/%s/operations/runbooks" (trimSuffix "/" .Values.alerting.runbookBaseURL) (include "muster.runbookVersion" .) }}
{{- end }}

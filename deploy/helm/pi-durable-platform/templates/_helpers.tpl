{{/* Names */}}
{{- define "pdp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pdp.fullname" -}}
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

{{- define "pdp.gatewayName" -}}
{{- printf "%s-gateway" (include "pdp.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pdp.metricsName" -}}
{{- printf "%s-metrics" (include "pdp.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pdp.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pdp.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Labels. The selector labels are what the NetworkPolicies match. */}}
{{- define "pdp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "pdp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "pdp.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "pdp.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* The pods the agents launcher creates carry these labels (launcher/kubernetes.go). */}}
{{- define "pdp.agentPodLabels" -}}
app.kubernetes.io/name: pi-durable-agent
{{- end -}}

{{/* Image references: repository:tag[@digest]; the tag defaults to v<appVersion>. */}}
{{- define "pdp.image" -}}
{{- $img := .image -}}
{{- $tag := default (printf "v%s" .root.Chart.AppVersion) $img.tag -}}
{{- if $img.digest -}}
{{- printf "%s:%s@%s" $img.repository $tag $img.digest -}}
{{- else -}}
{{- printf "%s:%s" $img.repository $tag -}}
{{- end -}}
{{- end -}}

{{- define "pdp.platformImage" -}}
{{- include "pdp.image" (dict "image" .Values.images.platform "root" .) -}}
{{- end -}}

{{- define "pdp.runtimeImage" -}}
{{- include "pdp.image" (dict "image" .Values.images.runtime "root" .) -}}
{{- end -}}

{{/* The agent-sandbox launcher claims runtimes from a warm pool. */}}
{{- define "pdp.warmPool" -}}
{{- if and (eq .Values.agents.launcher "agent-sandbox") .Values.agentSandbox.warmPool.enabled -}}true{{- end -}}
{{- end -}}

{{- define "pdp.gatewayUrl" -}}
{{- default (printf "http://%s.%s.svc:8099" (include "pdp.gatewayName" .) .Release.Namespace) .Values.agents.gatewayUrl -}}
{{- end -}}

{{/* A secretKeyRef env var: ref is a {name, key} of values.secrets; optional is true for optional keys. */}}
{{- define "pdp.secretEnv" -}}
- name: {{ .env }}
  valueFrom:
    secretKeyRef:
      name: {{ default .root.Values.secrets.name .ref.name }}
      key: {{ .ref.key }}
{{- if .optional }}
      optional: true
{{- end }}
{{- end -}}

{{/* NetworkPolicy peers for PostgreSQL (the same list for the platform and the agents). */}}
{{- define "pdp.databasePeers" -}}
{{- $db := .Values.networkPolicy.database -}}
{{- if and (not $db.podLabels) (not $db.cidrs) -}}
{{- fail "networkPolicy.database: set podLabels (the PostgreSQL pods) and/or cidrs, or turn networkPolicy.enabled off" -}}
{{- end -}}
{{- if $db.podLabels }}
- namespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: {{ default .Release.Namespace $db.namespace }}
  podSelector:
    matchLabels:
      {{- toYaml $db.podLabels | nindent 6 }}
{{- end }}
{{- range $db.cidrs }}
- ipBlock:
    cidr: {{ . }}
{{- end }}
{{- end -}}

{{/* The platform pods, as seen from the agents namespace. */}}
{{- define "pdp.platformPeer" -}}
- namespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: {{ .Release.Namespace }}
  podSelector:
    matchLabels:
      {{- include "pdp.selectorLabels" . | nindent 6 }}
{{- end -}}

{{/* Egress to cluster DNS. */}}
{{- define "pdp.dnsEgress" -}}
- to:
    - namespaceSelector:
        matchLabels:
          {{- toYaml .Values.networkPolicy.dns.namespaceSelector | nindent 10 }}
      podSelector:
        matchLabels:
          {{- toYaml .Values.networkPolicy.dns.podSelector | nindent 10 }}
  ports:
    - { protocol: UDP, port: 53 }
    - { protocol: TCP, port: 53 }
{{- end -}}

{{- define "agentcell.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" .Values.image.registry .component $tag -}}
{{- end -}}

{{- define "agentcell.labels" -}}
app.kubernetes.io/name: agentcell
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "agentcell.brokerURL" -}}
{{- if .Values.gitBroker.enabled -}}
http://{{ .Values.serviceAccount.brokerName }}.{{ .Values.controlNamespace }}.svc:8080
{{- end -}}
{{- end -}}

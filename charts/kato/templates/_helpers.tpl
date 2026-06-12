{{- define "kato.name" -}}kato{{- end -}}
{{- define "kato.labels" -}}
app.kubernetes.io/name: kato
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

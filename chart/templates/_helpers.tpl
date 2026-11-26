{{- define "keycloak.controllerImage" -}}
ghcr.io/bartlapp/bartl-keycloak-controller@sha256:cb040839e055b894d8c04a080f28d88732ef83d9db1604bf94dcee19c41a5496
{{- end -}}
{{- define "keycloak.backupImage" -}}
ghcr.io/bartlapp/bartl-keycloak-backup@sha256:d90bbd1821caecbcf0fb6acdbdcef4105344ccaee3a0aea37ee8cf1fb7d3c940
{{- end -}}
{{- define "keycloak.labels" -}}
app.kubernetes.io/name: bartl-keycloak
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end -}}

{{/* Validate only the dynamically selected interface, using the shipped JSON schema. */}}
{{- define "keycloak.validate" -}}
{{- $schema := .schema -}}
{{- if hasKey $schema "$ref" -}}
  {{- $schema = index .definitions (trimPrefix "#/definitions/" (index $schema "$ref")) -}}
{{- end -}}
{{- $value := .value -}}
{{- $path := .path -}}
{{- $type := $schema.type -}}
{{- $valid := false -}}
{{- if eq $type "object" -}}
  {{- $valid = kindIs "map" $value -}}
{{- else if eq $type "array" -}}
  {{- $valid = kindIs "slice" $value -}}
{{- else if eq $type "string" -}}
  {{- $valid = kindIs "string" $value -}}
{{- else if eq $type "boolean" -}}
  {{- $valid = kindIs "bool" $value -}}
{{- else if eq $type "integer" -}}
  {{- if or (kindIs "float64" $value) (kindIs "int" $value) (kindIs "int64" $value) -}}
    {{- $valid = eq (float64 $value) (floor (float64 $value)) -}}
  {{- end -}}
{{- end -}}
{{- if not $valid -}}{{- fail (printf "%s must be %s" $path $type) -}}{{- end -}}
{{- if eq $type "object" -}}
  {{- range $schema.required -}}
    {{- if not (hasKey $value .) -}}{{- fail (printf "%s.%s is required" $path .) -}}{{- end -}}
  {{- end -}}
  {{- if and (hasKey $schema "minProperties") (lt (len $value) (int $schema.minProperties)) -}}{{- fail (printf "%s has too few entries" $path) -}}{{- end -}}
  {{- if and (hasKey $schema "maxProperties") (gt (len $value) (int $schema.maxProperties)) -}}{{- fail (printf "%s has too many entries" $path) -}}{{- end -}}
  {{- $properties := $schema.properties | default dict -}}
  {{- range $key, $item := $value -}}
    {{- $child := dict -}}
    {{- if hasKey $properties $key -}}
      {{- $child = index $properties $key -}}
    {{- else if kindIs "map" $schema.additionalProperties -}}
      {{- $child = $schema.additionalProperties -}}
    {{- else if and (hasKey $schema "additionalProperties") (eq (toString $schema.additionalProperties) "false") -}}
      {{- fail (printf "%s.%s is unknown" $path $key) -}}
    {{- end -}}
    {{- if $child -}}
      {{- include "keycloak.validate" (dict "schema" $child "definitions" $.definitions "value" $item "path" (printf "%s.%s" $path $key)) -}}
    {{- end -}}
  {{- end -}}
{{- else if eq $type "array" -}}
  {{- if and (hasKey $schema "maxItems") (gt (len $value) (int $schema.maxItems)) -}}{{- fail (printf "%s has too many entries" $path) -}}{{- end -}}
  {{- if and $schema.uniqueItems (ne (len $value) (len (uniq $value))) -}}{{- fail (printf "%s must contain unique entries" $path) -}}{{- end -}}
  {{- range $index, $item := $value -}}
    {{- include "keycloak.validate" (dict "schema" $schema.items "definitions" $.definitions "value" $item "path" (printf "%s[%d]" $path $index)) -}}
  {{- end -}}
{{- else if eq $type "string" -}}
  {{- if and (hasKey $schema "minLength") (lt (len $value) (int $schema.minLength)) -}}{{- fail (printf "%s is too short" $path) -}}{{- end -}}
  {{- if and (hasKey $schema "maxLength") (gt (len $value) (int $schema.maxLength)) -}}{{- fail (printf "%s is too long" $path) -}}{{- end -}}
  {{- if and $schema.pattern (not (regexMatch $schema.pattern $value)) -}}{{- fail (printf "%s has an unsupported format" $path) -}}{{- end -}}
{{- else if eq $type "integer" -}}
  {{- if and (hasKey $schema "minimum") (lt (float64 $value) (float64 $schema.minimum)) -}}{{- fail (printf "%s is below its minimum" $path) -}}{{- end -}}
  {{- if and (hasKey $schema "maximum") (gt (float64 $value) (float64 $schema.maximum)) -}}{{- fail (printf "%s exceeds its maximum" $path) -}}{{- end -}}
{{- end -}}
{{- end -}}

{{- define "keycloak.defaults" -}}
database:
  secretName: keycloak-database
  adminSecretName: keycloak-database-admin
  size: 5Gi
bootstrapAdminSecret: keycloak-bootstrap
tls:
  managed: true
  acmeServer: https://acme-v02.api.letsencrypt.org/directory
ingress:
  enabled: true
  className: traefik
  tlsSecretName: keycloak-tls
  maintenance: false
  administratorSourceRanges: []
  controllerNamespace: kube-system
  controllerPodLabels:
    app.kubernetes.io/name: traefik
backup:
  readerSecret: keycloak-backup-reader
  writerSecret: keycloak-backup-writer
  schedule: "0 2 * * *"
  timeZone: UTC
  suspend: false
{{- end -}}

{{/* Resolve only the selected deployment; never write into shared installation values. */}}
{{- define "keycloak.selection" -}}
{{- $instance := .Values.instance | default "keycloak" -}}
{{- $intent := dict -}}
{{- if .Values.deployment -}}
  {{- $id := .Values.deployment -}}
  {{- $definitions := (mustFromJson (.Files.Get "interface.schema.json")).definitions -}}
  {{- if not (kindIs "map" .Values.deployments) -}}{{- fail "deployments must be an object when deployment is selected" -}}{{- end -}}
  {{- if not (hasKey .Values.deployments $id) -}}{{- fail (printf "deployments.%s is required" $id) -}}{{- end -}}
  {{- $deployment := index .Values.deployments $id -}}
  {{- include "keycloak.validate" (dict "schema" $definitions.deployment "definitions" $definitions "value" $deployment "path" (printf "deployments.%s" $id)) -}}
  {{- if and (hasKey .Values "instance") (ne .Values.instance $deployment.instance) -}}
    {{- fail (printf "instance %s conflicts with deployments.%s.instance %s" .Values.instance $id $deployment.instance) -}}
  {{- end -}}
  {{- $instance = $deployment.instance -}}
  {{- if ne $deployment.namespace .Release.Namespace -}}{{- fail (printf "deployments.%s.namespace %s does not match Helm namespace %s" $id $deployment.namespace .Release.Namespace) -}}{{- end -}}
  {{- if ne $deployment.release .Release.Name -}}{{- fail (printf "deployments.%s.release %s does not match Helm release %s" $id $deployment.release .Release.Name) -}}{{- end -}}
  {{- if not (hasKey .Values.services $instance) -}}{{- fail (printf "services.%s is required by deployments.%s.instance" $instance $id) -}}{{- end -}}
  {{- if not (kindIs "map" .Values.packages) -}}{{- fail "packages must be an object when deployment is selected" -}}{{- end -}}
  {{- if not (hasKey .Values.packages $deployment.package) -}}{{- fail (printf "packages.%s is required by deployments.%s.package" $deployment.package $id) -}}{{- end -}}
  {{- $package := index .Values.packages $deployment.package -}}
  {{- include "keycloak.validate" (dict "schema" $definitions.package "definitions" $definitions "value" $package "path" (printf "packages.%s" $deployment.package)) -}}
  {{- if ne $package.version .Chart.Version -}}{{- fail (printf "packages.%s.version selects %s but this chart is %s" $deployment.package $package.version .Chart.Version) -}}{{- end -}}
  {{- if hasKey .Values "operations" -}}
    {{- if not (kindIs "map" .Values.operations) -}}{{- fail "operations must be an object" -}}{{- end -}}
    {{- if hasKey .Values.operations "deployments" -}}
      {{- if not (kindIs "map" .Values.operations.deployments) -}}{{- fail "operations.deployments must be an object" -}}{{- end -}}
      {{- if hasKey .Values.operations.deployments $id -}}
        {{- $intent = index .Values.operations.deployments $id -}}
        {{- include "keycloak.validate" (dict "schema" $definitions.intent "definitions" $definitions "value" $intent "path" (printf "operations.deployments.%s" $id)) -}}
        {{- range $field := list "running" "replicas" -}}
          {{- if hasKey $intent $field -}}{{- fail (printf "operations.deployments.%s.%s is runtime intent; use the KeycloakApplication API" $id $field) -}}{{- end -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}
{{- toYaml (dict "instance" $instance "intent" $intent) -}}
{{- end -}}

{{/* Effective selected values; mergeOverwrite preserves false, zero and list replacement. */}}
{{- define "keycloak.service" -}}
{{- $selection := include "keycloak.selection" . | fromYaml -}}
{{- $instance := $selection.instance -}}
{{- $selected := dict -}}
{{- if hasKey .Values.services $instance -}}
  {{- $selected = index .Values.services $instance -}}
{{- end -}}
{{- $definitions := (mustFromJson (.Files.Get "interface.schema.json")).definitions -}}
{{- include "keycloak.validate" (dict "schema" $definitions.service "definitions" $definitions "value" $selected "path" (printf "services.%s" $instance)) -}}
{{- if and (hasKey $selected "release") (ne $selected.release .Chart.Version) -}}
  {{- fail (printf "services.%s.release selects %s but this chart is %s" $instance $selected.release .Chart.Version) -}}
{{- end -}}
{{- $service := mergeOverwrite (include "keycloak.defaults" . | fromYaml) (deepCopy $selected) -}}
{{- range $field, $path := dict "ingressEnabled" (list "ingress" "enabled") "maintenance" (list "ingress" "maintenance") "backupSuspend" (list "backup" "suspend") -}}
  {{- if hasKey $selection.intent $field -}}
    {{- $_ := set (index $service (first $path)) (last $path) (index $selection.intent $field) -}}
  {{- end -}}
{{- end -}}
{{- if not (hasKey $service "hostname") -}}
  {{- $root := required "dns.root is required when the selected Keycloak service has no hostname" (get (get .Values "dns" | default dict) "root") -}}
  {{- $_ := set $service "hostname" (printf "login.%s" $root) -}}
{{- end -}}
{{- if not (hasKey $service "realm") -}}{{- $_ := set $service "realm" .Values.installation -}}{{- end -}}
{{- include "keycloak.validate" (dict "schema" $definitions.service "definitions" $definitions "value" $service "path" (printf "services.%s" $instance)) -}}
{{- if and (hasKey $service.backup "endpoint") (hasKey $service.backup "repository") -}}
  {{- fail (printf "services.%s.backup must select either endpoint or repository, not both" $instance) -}}
{{- end -}}
{{- if and (or $service.ingress.enabled $service.tls.managed) (not $service.ingress.tlsSecretName) -}}
  {{- fail (printf "services.%s.ingress.tlsSecretName is required for ingress or managed TLS" $instance) -}}
{{- end -}}
{{- if and $service.ingress.maintenance (empty $service.ingress.administratorSourceRanges) -}}
  {{- fail (printf "services.%s.ingress.administratorSourceRanges is required for maintenance" $instance) -}}
{{- end -}}
{{- toYaml $service -}}
{{- end -}}

{{- define "keycloak.hostname" -}}
{{- printf "https://%s" .hostname -}}
{{- end -}}

{{- define "keycloak.issuer" -}}
{{- printf "%s/realms/%s" (include "keycloak.hostname" .) .realm -}}
{{- end -}}

{{/* Resolve deployment-only backup inputs; declarations never read resolved data. */}}
{{- define "keycloak.backup" -}}
{{- $root := .root -}}
{{- $service := .service -}}
{{- $desired := $service.backup -}}
{{- $instance := (include "keycloak.selection" $root | fromYaml).instance -}}
{{- $explicit := (index $root.Values.services $instance | default dict).backup | default dict -}}
{{- if and (hasKey $desired "enabled") (not $desired.enabled) -}}
  {{- toYaml (dict "enabled" false) -}}
{{- else -}}
  {{- $binding := dict -}}
  {{- if hasKey $root.Values "resolved" -}}
    {{- $resolved := $root.Values.resolved -}}
    {{- if not (kindIs "map" $resolved) -}}{{- fail "resolved must be an object" -}}{{- end -}}
    {{- if hasKey $resolved "backup" -}}
      {{- if not (kindIs "map" $resolved.backup) -}}{{- fail "resolved.backup must be an object" -}}{{- end -}}
      {{- if hasKey $resolved.backup "bindings" -}}
        {{- if not (kindIs "map" $resolved.backup.bindings) -}}{{- fail "resolved.backup.bindings must be an object" -}}{{- end -}}
        {{- if hasKey $resolved.backup.bindings $instance -}}
          {{- $binding = index $resolved.backup.bindings $instance -}}
          {{- $definitions := (mustFromJson ($root.Files.Get "interface.schema.json")).definitions -}}
          {{- include "keycloak.validate" (dict "schema" $definitions.binding "definitions" $definitions "value" $binding "path" (printf "resolved.backup.bindings.%s" $instance)) -}}
        {{- end -}}
      {{- end -}}
    {{- end -}}
  {{- end -}}
  {{- if hasKey $desired "endpoint" -}}
    {{- if or (not (hasKey $binding "endpoint")) (ne $desired.endpoint $binding.endpoint) -}}
      {{- fail (printf "services.%s.backup.endpoint requires a matching resolved.backup.bindings.%s.endpoint" $instance $instance) -}}
    {{- end -}}
    {{- if not (hasKey $binding "repository") -}}{{- fail "The selected backup endpoint binding must supply repository" -}}{{- end -}}
  {{- end -}}
  {{- range $key := list "endpoint" "repository" "enabled" -}}
    {{- if and (hasKey $explicit $key) (hasKey $binding $key) (ne (index $explicit $key) (index $binding $key)) -}}
      {{- fail (printf "services.%s.backup.%s conflicts with its resolved backup binding" $instance $key) -}}
    {{- end -}}
  {{- end -}}
  {{- if and (hasKey $binding "enabled") (not $binding.enabled) -}}
    {{- if or (hasKey $desired "endpoint") (hasKey $desired "repository") -}}{{- fail "A disabled resolved backup binding conflicts with the requested destination" -}}{{- end -}}
    {{- toYaml (dict "enabled" false) -}}
  {{- else -}}
    {{- $effective := deepCopy $desired -}}
    {{- range $key := list "repository" "readerSecret" "writerSecret" -}}
      {{- if and (hasKey $binding $key) (not (hasKey $explicit $key)) -}}
        {{- $_ := set $effective $key (index $binding $key) -}}
      {{- end -}}
    {{- end -}}
    {{- $enabled := hasKey $effective "repository" -}}
    {{- if and (or $desired.enabled $binding.enabled (hasKey $binding "endpoint")) (not $enabled) -}}{{- fail "Enabled Keycloak backup requires a repository or a resolved endpoint binding" -}}{{- end -}}
    {{- $_ := set $effective "enabled" $enabled -}}
    {{- toYaml $effective -}}
  {{- end -}}
{{- end -}}
{{- end -}}

#!/usr/bin/env bash
# Disposable single-node receiver; invoke with Bash from the repository root.
set -euo pipefail
: "${VALUES:?Source the installation connection.env first}" "${DEPLOYMENT:?}"
command -v yq >/dev/null
export DEPLOYMENT
INSTANCE=$(yq -er '.deployments[strenv(DEPLOYMENT)].instance' "$VALUES")
CLUSTER_ID=$(yq -er '.deployments[strenv(DEPLOYMENT)].cluster' "$VALUES")
NS=$(yq -er '.deployments[strenv(DEPLOYMENT)].namespace' "$VALUES")
export CLUSTER_ID
KUBE_CONTEXT=$(yq -er '.clusters[strenv(CLUSTER_ID)].kubeconfig.context' "$VALUES")
: "${WORK_DIR:?}"
KUBECONFIG=${KUBECONFIG:-"$WORK_DIR/clusters/$CLUSTER_ID/base/kubeconfig"}
export KUBECONFIG
kubectl --kubeconfig "$KUBECONFIG" config get-contexts "$KUBE_CONTEXT" -o name |
  jq -Rse --arg context "$KUBE_CONTEXT" 'split("\n") | index($context) != null' >/dev/null
INSTALL_DIR="$WORK_DIR/clusters/$CLUSTER_ID/applications/$INSTANCE"
if [ "$DEPLOYMENT" != "$INSTANCE" ]; then INSTALL_DIR="$INSTALL_DIR/deployments/$DEPLOYMENT"; fi
BACKUP_NS=${BACKUP_NS:-keycloak-repository}
TARGET_DEPLOYMENT=${TARGET_DEPLOYMENT:-keycloak-recovery}
export TARGET_DEPLOYMENT
# Prepare only missing synthetic replacement placement; preserve retained choices.
yq -i '
  .deployments[strenv(TARGET_DEPLOYMENT)] = ({
    "instance": .deployments[strenv(DEPLOYMENT)].instance,
    "cluster": .deployments[strenv(DEPLOYMENT)].cluster,
    "namespace": "keycloak-green",
    "release": .deployments[strenv(DEPLOYMENT)].release,
    "package": .deployments[strenv(DEPLOYMENT)].package
  } * (.deployments[strenv(TARGET_DEPLOYMENT)] // {}))
' "$VALUES"
RECOVERY_NS=$(yq -er '.deployments[strenv(TARGET_DEPLOYMENT)].namespace' "$VALUES")
test "$(yq -er '.deployments[strenv(TARGET_DEPLOYMENT)].cluster' "$VALUES")" = "$CLUSTER_ID"
test "$(yq -er '.deployments[strenv(TARGET_DEPLOYMENT)].instance' "$VALUES")" = "$INSTANCE"
LAB="$INSTALL_DIR/repository"
HOST="backup-store.$BACKUP_NS.svc"
WORKER=ghcr.io/bartlapp/bartl-keycloak-backup@sha256:c770a00be0eb661c4c10eefe5dbebe9ab755f7a2650398cd9172d9aa6f4897d7
# Use the same authoritative installation file for the source and synthetic target.
test -f "$VALUES"
KC_INSTANCE=$INSTANCE
REPOSITORY="rest:https://$HOST/keycloak/"
export KC_INSTANCE REPOSITORY
# Refuse to repoint an existing destination or credential reference. The fresh
# repository directory below also prevents regeneration of retained credentials.
yq -e '
  .services[strenv(KC_INSTANCE)].backup as $backup |
  ($backup.endpoint == null) and
  ($backup.repository == null or $backup.repository == strenv(REPOSITORY)) and
  ($backup.readerSecret == null or $backup.readerSecret == "keycloak-backup-reader") and
  ($backup.writerSecret == null or $backup.writerSecret == "keycloak-backup-writer") and
  (.resolved.backup.bindings[strenv(KC_INSTANCE)] == null)
' "$VALUES" >/dev/null
umask 077
mkdir "$LAB"
mkdir "$LAB/reader" "$LAB/writer"
printf '%s' "$(openssl rand -hex 32)" > "$LAB/writer/password"
cp "$LAB/writer/password" "$LAB/reader/password"
for role in writer reader; do
  printf '%s' "$role" > "$LAB/$role/username"
  printf '%s' "$(openssl rand -hex 32)" > "$LAB/$role/restPassword"
  printf '%s:%s\n' "$role" "$(openssl passwd -apr1 -stdin < "$LAB/$role/restPassword")" >> "$LAB/htpasswd"
done
openssl req -x509 -newkey rsa:3072 -nodes -days 7 \
  -keyout "$LAB/tls.key" -out "$LAB/tls.crt" -subj "/CN=$HOST" \
  -addext "subjectAltName=DNS:$HOST" -addext 'basicConstraints=critical,CA:TRUE' \
  2> "$LAB/certificate-generation.log"
cp "$LAB/tls.crt" "$LAB/reader/ca.crt"
cp "$LAB/tls.crt" "$LAB/writer/ca.crt"
cat > "$LAB/nginx.conf" <<'NGINX'
events {}
http {
  access_log off;
  map $remote_user $repository_backend {
    default http://127.0.0.1:8002;
    writer http://127.0.0.1:8000;
  }
  server {
    listen 8443 ssl;
    ssl_certificate /tls/tls.crt;
    ssl_certificate_key /tls/tls.key;
    client_max_body_size 0;
    location / { return 404; }
    location /keycloak/ {
      auth_basic "Keycloak lab repository";
      auth_basic_user_file /auth/htpasswd;
      proxy_request_buffering off;
      proxy_read_timeout 600s;
      proxy_pass $repository_backend;
    }
  }
  server {
    listen 127.0.0.1:8002;
    location ~ "^/keycloak/locks/[0-9a-f]{64}$" {
      limit_except GET POST DELETE { deny all; }
      proxy_pass http://127.0.0.1:8001;
    }
    location / {
      limit_except GET { deny all; }
      proxy_pass http://127.0.0.1:8001;
    }
  }
}
NGINX
kubectl --context "$KUBE_CONTEXT" create namespace "$BACKUP_NS"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" create secret tls receiver-tls \
  --cert="$LAB/tls.crt" --key="$LAB/tls.key"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" create secret generic receiver-auth --from-file="$LAB/htpasswd"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" create configmap receiver-nginx --from-file="$LAB/nginx.conf"
for role in reader writer; do
  kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" create secret generic "keycloak-backup-$role" --from-file="$LAB/$role/"
done
cat > "$LAB/receiver.yaml" <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: repository
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 10Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: backup-store
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels: {app: backup-store}
  template:
    metadata:
      labels: {app: backup-store}
    spec:
      automountServiceAccountToken: false
      containers:
        - name: writer
          image: docker.io/restic/rest-server@sha256:d2aff06f47eb38637dff580c3e6bce4af98f386c396a25d32eb6727ec96214a5
          command: [rest-server]
          args: [--path, /data, --listen, '127.0.0.1:8000', --no-auth, --append-only]
          volumeMounts:
            - {name: data, mountPath: /data}
        - name: reader
          image: docker.io/restic/rest-server@sha256:d2aff06f47eb38637dff580c3e6bce4af98f386c396a25d32eb6727ec96214a5
          command: [rest-server]
          args: [--path, /data, --listen, '127.0.0.1:8001', --no-auth]
          volumeMounts:
            - {name: data, mountPath: /data}
        - name: https
          image: docker.io/library/nginx:1.28.0
          ports: [{containerPort: 8443}]
          readinessProbe:
            tcpSocket: {port: 8443}
          volumeMounts:
            - {name: nginx, mountPath: /etc/nginx/nginx.conf, subPath: nginx.conf, readOnly: true}
            - {name: tls, mountPath: /tls, readOnly: true}
            - {name: auth, mountPath: /auth, readOnly: true}
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: repository}
        - name: nginx
          configMap: {name: receiver-nginx}
        - name: tls
          secret: {secretName: receiver-tls}
        - name: auth
          secret: {secretName: receiver-auth}
---
apiVersion: v1
kind: Service
metadata:
  name: backup-store
spec:
  selector: {app: backup-store}
  ports: [{port: 443, targetPort: 8443}]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: receiver
spec:
  podSelector:
    matchLabels: {app: backup-store}
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchExpressions:
              - key: kubernetes.io/metadata.name
                operator: In
                values: ["$NS", "$RECOVERY_NS", "$BACKUP_NS"]
      ports: [{protocol: TCP, port: 8443}]
EOF
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" apply -f "$LAB/receiver.yaml"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" rollout status deployment/backup-store --timeout=5m
cat > "$LAB/init.yaml" <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: repository-init
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 300
  template:
    spec:
      automountServiceAccountToken: false
      restartPolicy: Never
      securityContext: {runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532}
      initContainers:
        - name: wait-for-https
          image: docker.io/curlimages/curl:8.12.1
          command: [/bin/sh, -ec]
          args:
            - |
              status=\$(curl --silent --show-error --retry 20 --retry-connrefused \\
                --retry-delay 1 --retry-max-time 45 --max-time 3 \\
                --cacert /writer/ca.crt \\
                --user "\$(cat /writer/username):\$(cat /writer/restPassword)" \\
                --output /dev/null --write-out '%{http_code}' "https://$HOST/keycloak/config")
              test "\$status" = 404
          volumeMounts:
            - {name: writer, mountPath: /writer, readOnly: true}
      containers:
        - name: restic
          image: $WORKER
          command: [/bin/sh, -ec]
          args:
            - |
              export RESTIC_PASSWORD="\$(cat /writer/password)"
              export RESTIC_REST_USERNAME="\$(cat /writer/username)"
              export RESTIC_REST_PASSWORD="\$(cat /writer/restPassword)"
              restic --cacert /writer/ca.crt -r rest:https://$HOST/keycloak/ init
          volumeMounts:
            - {name: writer, mountPath: /writer, readOnly: true}
      volumes:
        - name: writer
          secret: {secretName: keycloak-backup-writer}
EOF
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" apply -f "$LAB/init.yaml"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" wait --for=condition=complete job/repository-init --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" logs job/repository-init
cat > "$LAB/probe.yaml" <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: repository-permissions
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 300
  template:
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      containers:
        - name: probe
          image: docker.io/curlimages/curl:8.12.1
          command: [/bin/sh, -ec]
          args:
            - |
              probe() {
                role=\$1 method=\$2 path=\$3 expected=\$4
                set --
                if [ "\$method" = HEAD ]; then set -- --head; fi
                if [ "\$method" = POST ]; then set -- --data-binary permission-probe; fi
                actual=\$(curl --silent --show-error --retry 20 --retry-connrefused \\
                  --retry-delay 1 --retry-max-time 45 --max-time 3 --cacert /\$role/ca.crt \\
                  --user "\$(cat /\$role/username):\$(cat /\$role/restPassword)" \\
                  "\$@" --request "\$method" --output /tmp/body --write-out '%{http_code}' \\
                  "https://$HOST/keycloak/\$path")
                test "\$actual" = "\$expected" || { echo "\$role \$method: expected \$expected, got \$actual"; exit 1; }
                echo "\$role \$method: \$actual"
              }
              probe reader GET config 200
              probe reader HEAD config 200
              probe reader POST data/reader-denied 403
              probe reader DELETE config 403
              probe reader POST snapshots/reader-denied 403
              probe reader POST locks/not-a-lock 403
              # SHA-256 of the POST body "permission-probe".
              lock=locks/83ede0da0e897b63bca6c60bf2210b412c581acca2682d3e4628f30a7edfb55e
              probe reader POST "\$lock" 200
              probe reader GET "\$lock" 200
              probe reader PUT "\$lock" 403
              probe reader DELETE "\$lock" 200
              probe writer DELETE config 403
              probe reader GET config 200
          volumeMounts:
            - {name: reader, mountPath: /reader, readOnly: true}
            - {name: writer, mountPath: /writer, readOnly: true}
      volumes:
        - name: reader
          secret: {secretName: keycloak-backup-reader}
        - name: writer
          secret: {secretName: keycloak-backup-writer}
EOF
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" apply -f "$LAB/probe.yaml"
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" wait --for=condition=complete job/repository-permissions --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$BACKUP_NS" logs job/repository-permissions
printf 'BACKUP_NS=%q\nTARGET_DEPLOYMENT=%q\nREPOSITORY=%q\n' \
  "$BACKUP_NS" "$TARGET_DEPLOYMENT" "rest:https://$HOST/keycloak/" > "$LAB/connection.env"
# Preserve explicit choices, including false, and all unrelated installation data.
yq -i '
  .services[strenv(KC_INSTANCE)].backup = ({
    "repository": strenv(REPOSITORY),
    "readerSecret": "keycloak-backup-reader",
    "writerSecret": "keycloak-backup-writer",
    "schedule": "0 2 * * *",
    "timeZone": "Etc/UTC",
    "suspend": true
  } * (.services[strenv(KC_INSTANCE)].backup // {}))
' "$VALUES"

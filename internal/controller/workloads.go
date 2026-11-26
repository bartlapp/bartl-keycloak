package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func ptr[T any](v T) *T { return &v }
func labels(a *api.KeycloakApplication, component string) map[string]string {
	return map[string]string{ownerLabel: string(a.UID), "app.kubernetes.io/name": component}
}
func metadata(a *api.KeycloakApplication, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: a.Namespace}
}
func secretEnv(env, secret, key string, optional bool) corev1.EnvVar {
	return corev1.EnvVar{Name: env, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key, Optional: ptr(optional)}}}
}
func (r *Reconciler) storage(ctx context.Context, a *api.KeycloakApplication) (*corev1.PersistentVolumeClaim, error) {
	size, err := resource.ParseQuantity(a.Spec.Database.Size)
	if err != nil || size.Sign() <= 0 {
		return nil, errors.New("database.size must be a positive Kubernetes quantity")
	}
	pvc := &corev1.PersistentVolumeClaim{}
	key := types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}
	binding := a.Spec.Identity.Installation + ":" + a.Spec.Identity.Instance + ":" + a.Spec.Database.SecretName + ":" + a.Spec.Database.AdminSecretName
	packageBinding := a.Spec.PackageRelease + "\n" + r.KeycloakImage + "\n" + r.PostgresImage + "\n" + r.ControllerImage + "\n" + r.BackupImage
	err = r.Get(ctx, key, pvc)
	if err == nil {
		if pvc.Labels[ownerLabel] != string(a.UID) || pvc.Annotations[bindingAnnotation] != binding {
			return nil, errors.New("retained PVC identity/credential binding differs; automatic adoption is forbidden")
		}
		if pvc.Annotations["keycloak.bartl.sh/package"] != packageBinding {
			return nil, errors.New("in-place package changes are not supported; replace on independent target storage")
		}
		requested := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if requested.Cmp(size) != 0 {
			return nil, errors.New("storage size change is not supported by this package")
		}
		return pvc, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	pvc = &corev1.PersistentVolumeClaim{ObjectMeta: metadata(a, "keycloak-db"), Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}}}}
	pvc.Labels = labels(a, "keycloak-db")
	pvc.Annotations = map[string]string{bindingAnnotation: binding, "keycloak.bartl.sh/package": packageBinding}
	// Deliberately no owner reference: uninstall and CR deletion must not garbage-collect the data.
	if err := r.authority(ctx, a); err != nil {
		return nil, err
	}
	if err = r.Create(ctx, pvc); err != nil {
		return nil, err
	}
	return pvc, nil
}
func (r *Reconciler) service(ctx context.Context, a *api.KeycloakApplication, name string, ports []corev1.ServicePort) error {
	s := &corev1.Service{ObjectMeta: metadata(a, name)}
	return r.apply(ctx, a, s, func() error {
		s.Labels = labels(a, name)
		s.Spec.Type = corev1.ServiceTypeClusterIP
		s.Spec.Selector = labels(a, name)
		s.Spec.Ports = ports
		return nil
	})
}
func (r *Reconciler) database(ctx context.Context, a *api.KeycloakApplication) error {
	if err := r.service(ctx, a, "keycloak-db", []corev1.ServicePort{{Name: "postgres", Port: 5432, TargetPort: intstr.FromInt32(5432)}}); err != nil {
		return err
	}
	s := &appsv1.StatefulSet{ObjectMeta: metadata(a, "keycloak-db")}
	return r.apply(ctx, a, s, func() error {
		if len(s.Spec.Template.Spec.Containers) > 0 && s.Spec.Template.Spec.Containers[0].Image != r.PostgresImage {
			return errors.New("database image transitions require a separately qualified procedure")
		}
		s.Labels = labels(a, "keycloak-db")
		s.Spec.ServiceName = "keycloak-db"
		s.Spec.Replicas = ptr(int32(1))
		s.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels(a, "keycloak-db")}
		s.Spec.Template = corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(a, "keycloak-db")}, Spec: corev1.PodSpec{ServiceAccountName: "keycloak-workload", AutomountServiceAccountToken: ptr(false), TerminationGracePeriodSeconds: ptr(int64(60)), Containers: []corev1.Container{{Name: "postgres", Image: r.PostgresImage, Command: []string{"/bin/sh", "-ec"}, Args: []string{postgresStart}, Env: []corev1.EnvVar{{Name: "PGDATA", Value: "/var/lib/postgresql/data/pgdata"}, secretEnv("POSTGRES_USER", a.Spec.Database.AdminSecretName, "username", true), secretEnv("POSTGRES_PASSWORD", a.Spec.Database.AdminSecretName, "password", true), {Name: "POSTGRES_DB", Value: "postgres"}, {Name: "POSTGRES_INITDB_ARGS", Value: "--auth-host=scram-sha-256 --auth-local=trust"}}, Ports: []corev1.ContainerPort{{Name: "postgres", ContainerPort: 5432}}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/postgresql/data"}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"/bin/sh", "-ec", "pg_isready -h 127.0.0.1 -p 5432"}}}, PeriodSeconds: 5}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "keycloak-db"}}}}}}
		s.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(999)), RunAsGroup: ptr(int64(999)), FSGroup: ptr(int64(999)),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
		s.Spec.Template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
		return nil
	})
}

// Native docker-entrypoint runs initdb only for a genuinely empty PGDATA. Existing nonempty
// directories without PG_VERSION, and incompatible native versions, are never initialized.
const postgresStart = `if [ -f "$PGDATA/PG_VERSION" ]; then
  [ "$(cat "$PGDATA/PG_VERSION")" = 17 ] || { echo 'Unsupported PostgreSQL data major version' >&2; exit 1; }
elif [ -d "$PGDATA" ] && [ -n "$(find "$PGDATA" -mindepth 1 -maxdepth 1 -print -quit)" ]; then
  echo 'Nonempty PostgreSQL data directory without PG_VERSION; refusing initdb' >&2; exit 1
fi
exec /usr/local/bin/docker-entrypoint.sh postgres
`

func (r *Reconciler) application(ctx context.Context, a *api.KeycloakApplication, binding string, initial, exclusive bool) error {
	if err := r.service(ctx, a, "keycloak", []corev1.ServicePort{{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)}}); err != nil {
		return err
	}
	d := &appsv1.Deployment{ObjectMeta: metadata(a, "keycloak")}
	return r.apply(ctx, a, d, func() error {
		if len(d.Spec.Template.Spec.Containers) > 0 && d.Spec.Template.Spec.Containers[0].Image != r.KeycloakImage {
			return errors.New("in-place Keycloak image transitions are not supported; replace on independent target storage")
		}
		d.Labels = labels(a, "keycloak")
		d.Annotations = map[string]string{bindingAnnotation: binding, initializationAnnotation: fmt.Sprint(exclusive)}
		var recovery recoveryOperation
		count := a.Spec.Application.Replicas
		if exclusive {
			count = 1
		}
		d.Spec.Replicas = &count
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels(a, "keycloak")}
		d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		annotations := d.Spec.Template.Annotations
		if annotations == nil {
			annotations = map[string]string{}
		}
		// Preserve kubectl.kubernetes.io/restartedAt (and other administrator annotations).
		// Native pool validation discards connections invalidated by PostgreSQL
		// restart before they can fail an otherwise ready login.
		env := []corev1.EnvVar{{Name: "KC_DB", Value: "postgres"}, {Name: "KC_DB_URL", Value: "jdbc:postgresql://keycloak-db:5432/$(BARTL_DATABASE)"}, secretEnv("KC_DB_USERNAME", a.Spec.Database.SecretName, "username", false), secretEnv("KC_DB_PASSWORD", a.Spec.Database.SecretName, "password", false), secretEnv("BARTL_DATABASE", a.Spec.Database.SecretName, "database", false), {Name: "BARTL_DATABASE_HOST", Value: "keycloak-db." + a.Namespace + ".svc"}, {Name: "BARTL_DATABASE_BINDING", Value: binding}, {Name: "BARTL_ALLOW_EMPTY", Value: fmt.Sprint(initial)}, {Name: "KC_HOSTNAME", Value: a.Spec.Configuration.Hostname}, {Name: "KC_HTTP_ENABLED", Value: "true"}, {Name: "KC_PROXY_HEADERS", Value: "xforwarded"}, {Name: "KC_HEALTH_ENABLED", Value: "true"}, {Name: "KC_METRICS_ENABLED", Value: "true"}, {Name: "KC_CACHE", Value: "ispn"}, {Name: "KC_CACHE_STACK", Value: "jdbc-ping"}, {Name: "KC_SHUTDOWN_DELAY", Value: "5s"}, {Name: "JAVA_OPTS_APPEND", Value: "-XX:MaxRAMPercentage=70.0 -Dquarkus.datasource.jdbc.validate-on-borrow=true"}}
		// Kubernetes env substitution is order-dependent.
		env[1], env[4] = env[4], env[1]
		pvc := &corev1.PersistentVolumeClaim{}
		if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, pvc); err != nil {
			return err
		}
		if raw := pvc.Annotations[recoveryAnnotation]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &recovery); err != nil {
				return errors.New("invalid recovery operation binding")
			}
			env = append(env, corev1.EnvVar{Name: "BARTL_RECOVERY_ID", Value: recovery.ID})
			if exclusive && !initial {
				d.Annotations[recoveryExecutionAnnotation] = recovery.ID
			}
		}
		if initial {
			env = append(env, secretEnv("KC_BOOTSTRAP_ADMIN_USERNAME", a.Spec.BootstrapAdminSecret, "username", true), secretEnv("KC_BOOTSTRAP_ADMIN_PASSWORD", a.Spec.BootstrapAdminSecret, "password", true))
		}
		probe := func(path string) *corev1.Probe {
			return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt32(9000)}}, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 3}
		}
		startup := probe("/health/started")
		startup.FailureThreshold = 120
		d.Spec.Template = corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(a, "keycloak"), Annotations: annotations}, Spec: corev1.PodSpec{ServiceAccountName: "keycloak-workload", AutomountServiceAccountToken: ptr(false), TerminationGracePeriodSeconds: ptr(int64(90)), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(1000)), FSGroup: ptr(int64(1000))}, InitContainers: []corev1.Container{{Name: "startup-guard", Image: r.ControllerImage, Command: []string{"/controller", "install"}, VolumeMounts: []corev1.VolumeMount{{Name: "guard", MountPath: "/shared"}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}}, Containers: []corev1.Container{{Name: "keycloak", Image: r.KeycloakImage, Command: []string{"/opt/bartl/controller", "keycloak-start"}, Env: env, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}, {Name: "management", ContainerPort: 9000}, {Name: "cluster", ContainerPort: 7800}}, VolumeMounts: []corev1.VolumeMount{{Name: "guard", MountPath: "/opt/bartl", ReadOnly: true}}, StartupProbe: startup, ReadinessProbe: probe("/health/ready"), LivenessProbe: probe("/health/live"), Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}}, Volumes: []corev1.Volume{{Name: "guard", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}
		return nil
	})
}

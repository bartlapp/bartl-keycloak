package controller

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	_ "time/tzdata"
)

func (r *Reconciler) removeBackup(ctx context.Context, a *api.KeycloakApplication) error {
	c := &batchv1.CronJob{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-backup"}, c); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !owned(c, a) {
		return errors.New("backup CronJob ownership collision")
	}
	// Remove the trigger, never its running or retained Jobs.
	return client.IgnoreNotFound(r.Delete(ctx, c, client.PropagationPolicy(metav1.DeletePropagationOrphan)))
}

func (r *Reconciler) backup(ctx context.Context, a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, binding string) error {
	b := a.Spec.Backup
	u, err := url.Parse(strings.TrimPrefix(b.Repository, "rest:"))
	if err != nil || !strings.HasPrefix(b.Repository, "rest:https://") || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("backup repository must be rest:https:// without embedded credentials, query or fragment")
	}
	if b.WriterSecret == "" || b.ReaderSecret == "" || b.Schedule == "" || b.TimeZone == "" || r.BackupImage == "" {
		return errors.New("backup requires writer/reader references, schedule, timeZone and pinned worker image")
	}
	if _, err := time.LoadLocation(b.TimeZone); err != nil {
		return errors.New("invalid backup timeZone")
	}
	field := func(name, path string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: path}}}
	}
	env := []corev1.EnvVar{
		{Name: "PGHOST", Value: "keycloak-db." + a.Namespace + ".svc"},
		secretEnv("PGUSER", a.Spec.Database.SecretName, "username", false),
		secretEnv("PGPASSWORD", a.Spec.Database.SecretName, "password", false),
		secretEnv("PGDATABASE", a.Spec.Database.SecretName, "database", false),
		{Name: "BARTL_DATABASE_BINDING", Value: binding},
		{Name: "BARTL_INSTALLATION", Value: a.Spec.Identity.Installation},
		{Name: "BARTL_INSTANCE", Value: a.Spec.Identity.Instance},
		{Name: "BARTL_APPLICATION_UID", Value: string(a.UID)},
		{Name: "BARTL_PVC_UID", Value: string(pvc.UID)},
		{Name: "BARTL_PACKAGE_RELEASE", Value: a.Spec.PackageRelease},
		{Name: "BARTL_KEYCLOAK_IMAGE", Value: r.KeycloakImage},
		{Name: "BARTL_POSTGRES_IMAGE", Value: r.PostgresImage},
		field("BARTL_OPERATION_UID", "metadata.labels['batch.kubernetes.io/controller-uid']"),
		field("BARTL_POD_UID", "metadata.uid"),
		{Name: "RESTIC_REPOSITORY", Value: b.Repository},
		secretEnv("RESTIC_PASSWORD", b.WriterSecret, "password", false),
		secretEnv("RESTIC_REST_USERNAME", b.WriterSecret, "username", false),
		secretEnv("RESTIC_REST_PASSWORD", b.WriterSecret, "restPassword", false),
		{Name: "HOME", Value: "/tmp"},
	}
	c := &batchv1.CronJob{ObjectMeta: metadata(a, "keycloak-backup")}
	return r.apply(ctx, a, c, func() error {
		c.Labels = labels(a, "keycloak-backup")
		c.Spec = batchv1.CronJobSpec{
			Schedule: b.Schedule, TimeZone: ptr(b.TimeZone), Suspend: ptr(b.Suspend),
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			StartingDeadlineSeconds:    ptr(int64(300)),
			SuccessfulJobsHistoryLimit: ptr(int32(3)), FailedJobsHistoryLimit: ptr(int32(3)),
			JobTemplate: batchv1.JobTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(a, "keycloak-backup")}, Spec: batchv1.JobSpec{
				BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: ptr(int64(1800)),
				Completions: ptr(int32(1)), Parallelism: ptr(int32(1)),
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(a, "keycloak-backup")}, Spec: corev1.PodSpec{
					ServiceAccountName: "keycloak-workload", AutomountServiceAccountToken: ptr(false),
					RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: ptr(int64(30)),
					SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(65532)), RunAsGroup: ptr(int64(65532)), FSGroup: ptr(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
					Containers: []corev1.Container{{Name: "backup", Image: r.BackupImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/backup"}, Env: env,
						SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "repository-ca", MountPath: "/repository-ca", ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "repository-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: b.WriterSecret, Optional: ptr(true), Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}}},
				}},
			}},
		}
		return nil
	})
}

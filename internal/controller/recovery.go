package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha2"
	"github.com/floriankesslergmbh/bartl-keycloak/internal/native"
	"github.com/jackc/pgx/v5"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const recoveryAnnotation = "keycloak.bartl.sh/recovery"
const recoveryJobAnnotation = "keycloak.bartl.sh/recovery-job"
const recoveryExecutionAnnotation = "keycloak.bartl.sh/recovery-execution"

type recoveryOperation struct {
	ID               string                `json:"id"`
	Generation       int64                 `json:"generation"`
	Binding          string                `json:"binding"`
	DatabaseIdentity string                `json:"databaseIdentity"`
	Configuration    recoveryConfiguration `json:"configuration"`
}
type recoveryConfiguration struct {
	Repository      string `json:"repository"`
	ReaderSecret    string `json:"readerSecret"`
	DatabaseSecret  string `json:"databaseSecret"`
	BootstrapSecret string `json:"bootstrapSecret"`
	Release         string `json:"release"`
	KeycloakImage   string `json:"keycloakImage"`
	PostgresImage   string `json:"postgresImage"`
	WorkerImage     string `json:"workerImage"`
	Hostname        string `json:"hostname"`
}
type recoveryExecution struct {
	Name string `json:"name"`
	UID  string `json:"uid,omitempty"`
}

func (r *Reconciler) recoveryConfiguration(a *api.KeycloakApplication) recoveryConfiguration {
	c := recoveryConfiguration{DatabaseSecret: a.Spec.Database.SecretName, BootstrapSecret: a.Spec.BootstrapAdminSecret, Release: a.Spec.PackageRelease, KeycloakImage: r.KeycloakImage, PostgresImage: r.PostgresImage, WorkerImage: r.BackupImage, Hostname: a.Spec.Configuration.Hostname}
	if a.Spec.Backup != nil {
		c.Repository = a.Spec.Backup.Repository
		c.ReaderSecret = a.Spec.Backup.ReaderSecret
	}
	return c
}
func (r *Reconciler) annotateRecovery(ctx context.Context, pvc *corev1.PersistentVolumeClaim, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if text, ok := value.(string); ok {
		payload = []byte(text)
	}
	before := pvc.DeepCopy()
	pvc.Annotations[key] = string(payload)
	return r.Patch(ctx, pvc, client.MergeFrom(before))
}
func (r *Reconciler) recovery(ctx context.Context, a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, binding string, conn *pgx.Conn, observation native.Observation, dep *appsv1.Deployment, exists bool) (bool, ctrl.Result, error) {
	record, err := native.ReadRecovery(ctx, conn, binding)
	fail := func(reason string, cause error) (bool, ctrl.Result, error) {
		if err := r.annotateRecovery(ctx, pvc, startupFailureAnnotation, reason+": "+cause.Error()); err != nil {
			return true, ctrl.Result{}, err
		}
		if a.Status.Operation != nil {
			a.Status.Operation.Result = "Failed"
		}
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
		}
		if err := r.removeIngress(ctx, a); err != nil {
			return true, ctrl.Result{}, err
		}
		result, err := r.report(ctx, a, observation.Classification, reason, cause.Error(), false, false, false, true)
		return true, result, err
	}
	if err != nil {
		return fail("RecoveryObservationRefused", err)
	}
	if pvc.Annotations[recoveryAnnotation] == "" {
		if record != nil {
			return fail("RecoveryBindingMissing", errors.New("native recovery has no retained controller operation"))
		}
		if observation.Classification != "Absent" || a.Spec.Backup == nil || a.Spec.Application.Replicas == 0 {
			return false, ctrl.Result{}, nil
		}
		config := r.recoveryConfiguration(a)
		u, err := url.Parse(strings.TrimPrefix(config.Repository, "rest:"))
		if err != nil || !strings.HasPrefix(config.Repository, "rest:https://") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || config.ReaderSecret == "" || config.WorkerImage == "" {
			return fail("RecoveryInputsRequired", errors.New("recovery requires an HTTPS repository, reader Secret and pinned worker image"))
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return fail("RecoveryBindingFailed", err)
		}
		op := recoveryOperation{ID: hex.EncodeToString(id[:]), Generation: a.Generation, Binding: binding, DatabaseIdentity: observation.Identity, Configuration: config}
		if err := r.annotateRecovery(ctx, pvc, recoveryAnnotation, op); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	var op recoveryOperation
	if err := json.Unmarshal([]byte(pvc.Annotations[recoveryAnnotation]), &op); err != nil || len(op.ID) != 32 || op.Binding != binding || op.DatabaseIdentity != observation.Identity {
		return fail("RecoveryBindingMismatch", errors.New("retained recovery operation does not match the native target"))
	}
	if record != nil && (record.OperationID != op.ID || record.Binding != binding || record.DatabaseIdentity != op.DatabaseIdentity || record.ApplicationUID != string(a.UID) || record.PVCUID != string(pvc.UID) || record.Installation != a.Spec.Identity.Installation || record.Instance != a.Spec.Identity.Instance) {
		return fail("RecoveryBindingMismatch", errors.New("native outcome belongs to another recovery operation"))
	}
	jobIdentity := api.OperationJob{}
	if previous := a.Status.Operation; previous != nil && previous.ID == op.ID {
		jobIdentity = previous.Job
	}
	if raw := pvc.Annotations[recoveryJobAnnotation]; raw != "" {
		var execution recoveryExecution
		if err := json.Unmarshal([]byte(raw), &execution); err != nil || execution.Name != "keycloak-recover-"+op.ID[:16] {
			return fail("RecoveryJobBindingRefused", errors.New("retained recovery Job identity is malformed"))
		}
		jobIdentity = api.OperationJob{Name: execution.Name, UID: execution.UID}
	}
	a.Status.Operation = &api.Operation{ID: op.ID, Kind: "Recovery", InitiatedGeneration: op.Generation, Target: api.OperationTarget{PVCUID: string(pvc.UID), DatabaseIdentity: op.DatabaseIdentity}, Job: jobIdentity, Result: "Executing"}
	if record != nil {
		a.Status.Operation.Result = record.Phase
		a.Status.Operation.Snapshot = api.OperationSnapshot{RepositoryIdentity: record.RepositoryID, ID: record.SnapshotID}
		if record.Phase == "Complete" {
			return false, ctrl.Result{}, nil
		}
	}
	if err := r.removeIngress(ctx, a); err != nil {
		return true, ctrl.Result{}, err
	}
	if !reflect.DeepEqual(op.Configuration, r.recoveryConfiguration(a)) {
		return fail("RecoveryConfigurationChanged", errors.New("pending recovery inputs differ from the immutable operation"))
	}
	if record == nil && exists {
		if err := r.scale(ctx, dep, 0); err != nil {
			return true, ctrl.Result{}, err
		}
		pods, err := r.applicationPods(ctx, a, dep)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if len(pods) != 0 {
			result, err := r.report(ctx, a, observation.Classification, "RecoveryDraining", "Waiting for every Keycloak process to terminate before recovery", false, false, true, false)
			return true, result, err
		}
	}
	job, err := r.recoveryJob(ctx, a, pvc, op)
	if err != nil {
		return fail("RecoveryJobRefused", err)
	}
	a.Status.Operation.Job = api.OperationJob{Name: job.Name, UID: string(job.UID)}
	complete := false
	for _, condition := range job.Status.Conditions {
		if condition.Status == corev1.ConditionTrue {
			if condition.Type == batchv1.JobFailed {
				return fail("RecoveryJobFailed", errors.New("finite recovery Job failed; native commit is not permission to continue"))
			}
			complete = complete || condition.Type == batchv1.JobComplete
		}
	}
	if complete {
		if record == nil {
			return fail("RecoveryOutcomeMissing", errors.New("successful recovery Job has no bound native result"))
		}
		switch record.Phase {
		case "NoBackup":
			return false, ctrl.Result{}, nil
		case "Restored":
			return r.recoveredStartup(ctx, a, pvc, binding, conn, dep, exists, op, fail)
		default:
			return fail("RecoveryPhaseRefused", errors.New("unexpected native recovery result"))
		}
	}
	if exists {
		if err := r.scale(ctx, dep, 0); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	result, err := r.report(ctx, a, observation.Classification, "RecoveryExecuting", "One finite recovery Job selects and restores; application access remains withheld", false, false, true, false)
	return true, result, err
}
func (r *Reconciler) recoveryJob(ctx context.Context, a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, op recoveryOperation) (*batchv1.Job, error) {
	name := "keycloak-recover-" + op.ID[:16]
	execution := recoveryExecution{}
	raw := pvc.Annotations[recoveryJobAnnotation]
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &execution); err != nil {
			return nil, errors.New("invalid retained Job binding")
		}
	}
	job := &batchv1.Job{}
	err := r.Reader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: name}, job)
	if err == nil {
		if !owned(job, a) || job.Annotations[recoveryAnnotation] != pvc.Annotations[recoveryAnnotation] || execution.Name != name || (execution.UID != "" && execution.UID != string(job.UID)) {
			return nil, errors.New("recovery Job ownership, immutable configuration or UID mismatch")
		}
		// Kubernetes keeps this Job's execution template immutable. Its retained
		// UID and operation binding identify the admitted execution.
	} else {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		if raw != "" {
			return nil, errors.New("admitted recovery Job " + name + " is missing for operation " + op.ID + "; native outcome is not established, so retain isolation and inspect the retained admission and database before any retry")
		}
		execution = recoveryExecution{Name: name}
		if err = r.annotateRecovery(ctx, pvc, recoveryJobAnnotation, execution); err != nil {
			return nil, err
		}
		job, err = r.recoveryJobSpec(a, pvc, op)
		if err != nil {
			return nil, err
		}
		if err = r.authority(ctx, a); err != nil {
			return nil, err
		}
		if err = controllerutil.SetControllerReference(a, job, r.Scheme); err != nil {
			return nil, err
		}
		if err = r.Create(ctx, job); err != nil {
			return nil, err
		}
	}
	if execution.UID == "" {
		execution.UID = string(job.UID)
		if err = r.annotateRecovery(ctx, pvc, recoveryJobAnnotation, execution); err != nil {
			return nil, err
		}
	}
	return job, nil
}
func (r *Reconciler) recoveryJobSpec(a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, op recoveryOperation) (*batchv1.Job, error) {
	c := op.Configuration
	env := []corev1.EnvVar{{Name: "PGHOST", Value: "keycloak-db." + a.Namespace + ".svc"}, secretEnv("PGUSER", c.DatabaseSecret, "username", false), secretEnv("PGPASSWORD", c.DatabaseSecret, "password", false), secretEnv("PGDATABASE", c.DatabaseSecret, "database", false), {Name: "BARTL_DATABASE_BINDING", Value: op.Binding}, {Name: "BARTL_INSTALLATION", Value: a.Spec.Identity.Installation}, {Name: "BARTL_INSTANCE", Value: a.Spec.Identity.Instance}, {Name: "BARTL_APPLICATION_UID", Value: string(a.UID)}, {Name: "BARTL_PVC_UID", Value: string(pvc.UID)}, {Name: "BARTL_PACKAGE_RELEASE", Value: c.Release}, {Name: "BARTL_KEYCLOAK_IMAGE", Value: c.KeycloakImage}, {Name: "BARTL_POSTGRES_IMAGE", Value: c.PostgresImage}, {Name: "BARTL_RECOVERY_ID", Value: op.ID}, {Name: "RESTIC_REPOSITORY", Value: c.Repository}, secretEnv("RESTIC_PASSWORD", c.ReaderSecret, "password", false), secretEnv("RESTIC_REST_USERNAME", c.ReaderSecret, "username", false), secretEnv("RESTIC_REST_PASSWORD", c.ReaderSecret, "restPassword", false), {Name: "HOME", Value: "/tmp"}}
	job := &batchv1.Job{ObjectMeta: metadata(a, "keycloak-recover-"+op.ID[:16])}
	job.Annotations = map[string]string{recoveryAnnotation: pvc.Annotations[recoveryAnnotation]}
	job.Labels = labels(a, "keycloak-backup")
	job.Spec = batchv1.JobSpec{BackoffLimit: ptr(int32(0)), ActiveDeadlineSeconds: ptr(int64(1800)), Completions: ptr(int32(1)), Parallelism: ptr(int32(1)), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(a, "keycloak-backup")}, Spec: corev1.PodSpec{ServiceAccountName: "keycloak-workload", AutomountServiceAccountToken: ptr(false), RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: ptr(int64(30)), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(65532)), RunAsGroup: ptr(int64(65532)), FSGroup: ptr(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "recovery", Image: c.WorkerImage, Command: []string{"/backup"}, Args: []string{"recover"}, Env: env, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "repository-ca", MountPath: "/repository-ca", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "repository-ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: c.ReaderSecret, Optional: ptr(true), Items: []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}}}}}}}}}
	return job, nil
}

func (r *Reconciler) recoveredStartup(ctx context.Context, a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, binding string, conn *pgx.Conn, dep *appsv1.Deployment, exists bool, op recoveryOperation, fail func(string, error) (bool, ctrl.Result, error)) (bool, ctrl.Result, error) {
	latest := new(api.KeycloakApplication)
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
		return true, ctrl.Result{}, err
	}
	if latest.Generation != a.Generation || !latest.Spec.Running || latest.Spec.Application.Replicas == 0 {
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
		}
		result, err := r.report(ctx, a, "Restored", "RecoveryStopped", "Native restore is verified; persistent stop withholds startup", false, true, false, false)
		return true, result, err
	}
	admitted := pvc.Annotations[recoveryExecutionAnnotation]
	if admitted != "" && admitted != op.ID {
		return fail("RecoveryStartupRefused", errors.New("exclusive startup belongs to another recovery"))
	}
	if admitted == op.ID && !exists {
		return fail("RecoveryStartupInterrupted", errors.New("admitted recovery workload is missing; refusing replay"))
	}
	if exists {
		pods, err := r.applicationPods(ctx, a, dep)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		starting := dep.Annotations[recoveryExecutionAnnotation] == op.ID && dep.Spec.Replicas != nil && *dep.Spec.Replicas == 1
		record, err := native.ReadRecovery(ctx, conn, binding)
		if err != nil {
			return fail("RecoveryObservationRefused", err)
		}
		if record != nil && record.StartupStarted && (!starting || len(pods) == 0) {
			return fail("RecoveryStartupInterrupted", errors.New("claimed recovery startup has no surviving process; refusing replay"))
		}
		if len(pods) != 0 && (!starting || len(pods) == 1 && !pods[0].DeletionTimestamp.IsZero()) {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
			result, err := r.report(ctx, a, "Restored", "RecoveryDraining", "Waiting for all old Keycloak Pods to disappear before exclusive startup", false, false, true, false)
			return true, result, err
		}
		if starting && len(pods) > 1 {
			return fail("RecoveryStartupRefused", errors.New("more than one recovery startup process exists"))
		}
		if starting {
			_, healthy, err := r.initializing(ctx, a, dep)
			if err != nil {
				return true, ctrl.Result{}, err
			}
			if !healthy {
				return fail("RecoveryStartupInterrupted", errors.New("exclusive native startup terminated before verification; refusing migration replay"))
			}
		}
		if starting && len(pods) == 1 && pods[0].DeletionTimestamp.IsZero() && dep.Status.ObservedGeneration == dep.Generation && dep.Status.ReadyReplicas == 1 {
			if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
				return true, ctrl.Result{}, err
			}
			if latest.Generation != a.Generation || !latest.Spec.Running || latest.Spec.Application.Replicas == 0 {
				if err := r.scale(ctx, dep, 0); err != nil {
					return true, ctrl.Result{}, err
				}
				return true, ctrl.Result{}, nil
			}
			if err := native.CompleteRecovery(ctx, conn, binding, op.ID); err != nil {
				return fail("RecoveryVerificationFailed", err)
			}
			result, err := r.report(ctx, a, "Valid", "RecoveryComplete", "Native recovery startup verified", false, false, true, false)
			return true, result, err
		}
		if starting {
			result, err := r.report(ctx, a, "Restored", "RecoveryStarting", "Waiting for the admitted exclusive startup; terminating Pods cannot authorize completion", false, false, true, false)
			return true, result, err
		}
	}
	if admitted == "" {
		if err := r.annotateRecovery(ctx, pvc, recoveryExecutionAnnotation, op.ID); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
		return true, ctrl.Result{}, err
	}
	if latest.Generation != a.Generation || !latest.Spec.Running || latest.Spec.Application.Replicas == 0 {
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
		}
		return true, ctrl.Result{}, nil
	}
	if err := r.application(ctx, a, binding, false, true); err != nil {
		return fail("RecoveryStartupRefused", err)
	}
	result, err := r.report(ctx, a, "Restored", "RecoveryStarting", "One exclusive native startup admitted; routing remains withheld until native verification", false, false, true, false)
	return true, result, err
}

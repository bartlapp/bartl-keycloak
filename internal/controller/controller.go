package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"time"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha1"
	"github.com/floriankesslergmbh/bartl-keycloak/internal/native"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const ownerLabel = "keycloak.bartl.sh/owner"
const bindingAnnotation = "keycloak.bartl.sh/binding"
const initializationAnnotation = "keycloak.bartl.sh/initialization"
const establishedAnnotation = "keycloak.bartl.sh/established"
const startupFailureAnnotation = "keycloak.bartl.sh/startup-failure"

type Reconciler struct {
	client.Client
	Reader                                                                      client.Reader
	Scheme                                                                      *runtime.Scheme
	Namespace, Name, KeycloakImage, PostgresImage, ControllerImage, BackupImage string
}

func (r *Reconciler) Setup(m ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(m).For(&api.KeycloakApplication{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Owns(&appsv1.Deployment{}).Owns(&appsv1.StatefulSet{}).Owns(&batchv1.CronJob{}).Owns(&batchv1.Job{}).Complete(r)
}
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, reconcileErr error) {
	if req.Namespace != r.Namespace || req.Name != r.Name {
		return ctrl.Result{}, nil
	}
	a := new(api.KeycloakApplication)
	if err := r.Get(ctx, req.NamespacedName, a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !a.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	previous := a.DeepCopyObject().(*api.KeycloakApplication)
	defer func() {
		if !reflect.DeepEqual(previous.Status, a.Status) {
			reconcileErr = errors.Join(reconcileErr, r.Status().Update(ctx, a))
		}
	}()
	result = ctrl.Result{RequeueAfter: 5 * time.Second}
	dep := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak"}, dep)
	if err != nil && !apierrors.IsNotFound(err) {
		return result, err
	}
	exists := err == nil
	if exists && !owned(dep, a) {
		return r.report(ctx, a, "Unknown", "OwnershipCollision", "Deployment keycloak is not owned by this application", false, false, false, true)
	}
	// Stop wins even over malformed/unsupported new inputs and missing credentials.
	if a.Spec.Application.Replicas == 0 && exists {
		if err := r.scale(ctx, dep, 0); err != nil {
			return result, err
		}
	}
	fail := func(class, reason, message string) (ctrl.Result, error) {
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return result, err
			}
		}
		if err := r.removeIngress(ctx, a); err != nil {
			message += "; ingress withdrawal failed"
		}
		return r.report(ctx, a, class, reason, message, false, false, false, true)
	}
	if err := validate(a); err != nil {
		return fail("Unknown", "UnsupportedConfiguration", err.Error())
	}
	if a.Spec.Backup == nil {
		if err := r.removeBackup(ctx, a); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Backup schedule removal refused")
		}
	}
	pvc, err := r.storage(ctx, a)
	if err != nil {
		return fail("Unknown", "StorageRefused", err.Error())
	}
	binding := fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", a.Spec.Identity.Installation, a.Spec.Identity.Instance, a.UID, pvc.UID)
	if exists && dep.Annotations[bindingAnnotation] != binding {
		return fail("Unknown", "TargetBindingMismatch", "Existing application execution is bound to another native target")
	}
	if err := r.policies(ctx, a); err != nil {
		return fail("Unknown", "IsolationRefused", err.Error())
	}
	if err := r.database(ctx, a); err != nil {
		return fail("Unknown", "DatabaseWorkloadRefused", err.Error())
	}
	app, err := r.credentials(ctx, a.Namespace, a.Spec.Database.SecretName, true)
	if err != nil {
		return fail("Unknown", "DatabaseCredentialsUnavailable", err.Error())
	}
	databaseBinding := app.Database + "\n" + app.Username
	const databaseAnnotation = "keycloak.bartl.sh/database"
	if previous := pvc.Annotations[databaseAnnotation]; previous != "" && previous != databaseBinding {
		return fail("Unknown", "DatabaseBindingChanged", "Secret contents redirect the bound database or role; refusing mutation")
	}
	if pvc.Annotations[databaseAnnotation] == "" {
		before := pvc.DeepCopy()
		pvc.Annotations[databaseAnnotation] = databaseBinding
		if err := r.Patch(ctx, pvc, client.MergeFrom(before)); err != nil {
			return result, err
		}
	}
	if a.Spec.Backup != nil {
		if err := r.backup(ctx, a, pvc, binding); err != nil {
			// Backup configuration and repository access never revoke local service.
			ctrl.LoggerFrom(ctx).Error(err, "Backup schedule reconciliation refused")
		}
	}
	// Established reconciliation owns desired resources and readiness, not the
	// database schema. Observer outages cannot terminate healthy Keycloak.
	if pvc.Annotations[establishedAnnotation] == r.KeycloakImage && exists &&
		len(dep.Spec.Template.Spec.Containers) == 1 && dep.Spec.Template.Spec.Containers[0].Image == r.KeycloakImage {
		latest := new(api.KeycloakApplication)
		if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
			return result, err
		}
		if latest.Generation != a.Generation {
			return result, nil
		}
		if err := r.application(ctx, a, binding, false, false, false); err != nil {
			return fail("Unknown", "ApplicationWorkloadRefused", err.Error())
		}
		if err := r.routing(ctx, a); err != nil {
			return fail("Unknown", "RoutingRefused", err.Error())
		}
		return r.report(ctx, a, "Valid", "NativeStartupAvailable", "Established application workload reconciled", a.Spec.Application.Replicas > 0, a.Spec.Application.Replicas == 0, false, false)
	}
	if reason := pvc.Annotations[startupFailureAnnotation]; reason != "" {
		return fail("Incomplete", "StartupInterrupted", reason+"; discard the failed target before retry")
	}
	host := "keycloak-db." + a.Namespace + ".svc"
	dbctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := native.Connect(dbctx, host, app)
	if err != nil {
		var db appsv1.StatefulSet
		if getErr := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, &db); getErr != nil {
			return result, getErr
		}
		if db.Status.ReadyReplicas != 1 {
			if exists {
				if err := r.scale(ctx, dep, 0); err != nil {
					return result, err
				}
			}
			if err := r.removeIngress(ctx, a); err != nil {
				return result, err
			}
			return r.report(ctx, a, "Unknown", "WaitingForPostgreSQL", "PostgreSQL is not ready; existing storage is never interpreted as empty", false, false, true, false)
		}
		admin, secretErr := r.credentials(ctx, a.Namespace, a.Spec.Database.AdminSecretName, false)
		if secretErr != nil {
			return fail("Unknown", "DatabaseAdministrationRequired", "Application database cannot be reached; native absent-database observation needs the database administrator Secret")
		}
		if err = native.Provision(dbctx, host, admin, app, binding); err != nil {
			return fail("Unknown", "DatabaseProvisionRefused", err.Error())
		}
		conn, err = native.Connect(dbctx, host, app)
		if err != nil {
			return fail("Unknown", "DatabaseUnavailable", "Newly provisioned database connection failed")
		}
	}
	defer conn.Close(ctx)
	var locked bool
	if err := conn.QueryRow(dbctx, "SELECT pg_try_advisory_lock(748920164)").Scan(&locked); err != nil {
		return r.report(ctx, a, "Unknown", "NativeExclusionUnavailable", "Native operation exclusion unavailable; no transition admitted", false, a.Spec.Application.Replicas == 0, true, false)
	}
	if !locked {
		// This target has not completed the selected startup. Access withdrawal
		// must not wait for a backup or another native operation to release its lock.
		if err := r.removeIngress(ctx, a); err != nil {
			return result, err
		}
		return r.report(ctx, a, "Unknown", "NativeOperationActive", "A native operation is executing; no conflicting transition admitted", false, a.Spec.Application.Replicas == 0, true, false)
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(748920164)")
	observation, err := native.Observe(dbctx, conn, binding)
	if err != nil {
		return fail("Unknown", "NativeObservationFailed", "Native database observation failed: "+err.Error())
	}
	if handled, recoveryResult, recoveryErr := r.recovery(ctx, a, pvc, binding, conn, observation, dep, exists); handled {
		return recoveryResult, recoveryErr
	}
	if handled, upgradeResult, upgradeErr := r.upgrade(ctx, a, pvc, binding, conn, observation, dep, exists); handled {
		return upgradeResult, upgradeErr
	}
	if a.Spec.Application.Replicas == 0 {
		if err := r.routing(ctx, a); err != nil {
			return fail("Unknown", "RoutingRefused", err.Error())
		}
		quiescent := observation.Classification == "Valid" || observation.Classification == "Absent"
		return r.report(ctx, a, observation.Classification, observation.Reason, "Persistent stop requested; PostgreSQL remains available", false, true, false, !quiescent)
	}
	if observation.Classification == "Unverified" {
		if !exists || dep.Annotations[initializationAnnotation] != "true" {
			return fail("Incomplete", "UnverifiedNativeSchema", "Complete-looking native state has no verified exclusive first-start execution")
		}
		active, healthy, err := r.initializing(ctx, a, dep)
		if err != nil {
			return fail("Unknown", "StartupObservationFailed", err.Error())
		}
		if !active || !healthy {
			if err := r.annotateRecovery(ctx, pvc, startupFailureAnnotation, "Exclusive initialization ended before readiness"); err != nil {
				return result, err
			}
			return fail("Incomplete", "InitializationInterrupted", "Exclusive first startup ended before readiness; refusing replay")
		}
		if dep.Status.ObservedGeneration != dep.Generation || dep.Status.ReadyReplicas != 1 {
			return r.report(ctx, a, "Unverified", observation.Reason, "Waiting for exclusive native startup readiness", false, false, true, false)
		}
		if err := native.CompleteInitialization(dbctx, conn, binding); err != nil {
			return fail("Unknown", "NativeVerificationFailed", err.Error())
		}
		if a.Status.Operation != nil && a.Status.Operation.Result == "NoBackup" {
			a.Status.Operation.Result = "Complete"
		}
		observation, err = native.Observe(dbctx, conn, binding)
		if err != nil {
			return fail("Unknown", "NativeVerificationFailed", err.Error())
		}
		if observation.Classification != "Valid" {
			return fail("Incomplete", observation.Reason, "Native first-start verification did not establish compatible state")
		}
	}
	initial := observation.Classification == "Absent"
	if observation.Classification == "Incomplete" {
		// A live, first-start execution may still be completing native initialization. Never admit another one.
		if exists && dep.Annotations[initializationAnnotation] == "true" {
			active, healthy, err := r.initializing(ctx, a, dep)
			if err != nil {
				return fail("Unknown", "StartupObservationFailed", err.Error())
			}
			if active && healthy {
				return r.report(ctx, a, "Incomplete", observation.Reason, "Exclusive native first startup is still executing", false, false, true, false)
			}
		}
		return fail("Incomplete", observation.Reason, "Native state is partial or incompatible; no initialization or repair will be replayed")
	}
	if initial {
		if a.Spec.Backup != nil && pvc.Annotations[recoveryAnnotation] == "" {
			return fail("Absent", "RecoveryBindingRequired", "Configured repository requires a verified native recovery selection")
		}
		if exists {
			active, healthy, err := r.initializing(ctx, a, dep)
			if err != nil {
				return fail("Unknown", "StartupObservationFailed", err.Error())
			}
			if dep.Annotations[initializationAnnotation] != "true" || (active && !healthy) {
				if err := r.annotateRecovery(ctx, pvc, startupFailureAnnotation, "Admitted initialization was interrupted"); err != nil {
					return result, err
				}
				return fail("Incomplete", "InitializationInterrupted", "An existing execution cannot safely reinitialize this target")
			}
		}
		if _, err := r.credentials(ctx, a.Namespace, a.Spec.BootstrapAdminSecret, false); err != nil {
			return fail("Absent", "BootstrapCredentialsRequired", "Confirmed empty database requires the bootstrap administrator Secret")
		}
	}
	exclusive := initial
	// Re-read intent before admitting any process. Status and a stale cached generation are not admission authority.
	latest := new(api.KeycloakApplication)
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
		return result, err
	}
	if latest.Generation != a.Generation || latest.Spec.Application.Replicas == 0 {
		return result, nil
	}
	if exclusive {
		if err := r.removeIngress(ctx, a); err != nil {
			return fail("Unknown", "RoutingRefused", err.Error())
		}
	} else {
		if err := r.routing(ctx, a); err != nil {
			return fail("Unknown", "RoutingRefused", err.Error())
		}
	}
	if err := r.application(ctx, a, binding, initial, exclusive, false); err != nil {
		return fail("Unknown", "ApplicationWorkloadRefused", err.Error())
	}
	if exclusive {
		return r.report(ctx, a, observation.Classification, "Initializing", "One exclusive native Keycloak startup is admitted", false, false, true, false)
	}
	if err := r.annotateRecovery(ctx, pvc, establishedAnnotation, r.KeycloakImage); err != nil {
		return result, err
	}
	return r.report(ctx, a, "Valid", "NativeStartupAvailable", "Native startup and application readiness observed", true, false, false, false)
}
func validate(a *api.KeycloakApplication) error {
	if a.Name != "keycloak" || a.Spec.Identity.Installation == "" || a.Spec.Identity.Instance == "" || a.Spec.PackageRelease == "" || a.Spec.Application.Replicas < 0 {
		return errors.New("invalid identity, release or replica intent")
	}
	u, err := url.Parse(a.Spec.Configuration.Hostname)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("hostname must be an HTTPS origin without path, credentials, query or fragment")
	}
	if a.Spec.Database.SecretName == "" || a.Spec.Database.AdminSecretName == "" {
		return errors.New("database Secret references are required")
	}
	if a.Spec.Ingress.Enabled && (a.Spec.Ingress.ClassName != "traefik" || a.Spec.Ingress.TLSSecretName == "" || a.Spec.Ingress.ControllerNamespace == "" || len(a.Spec.Ingress.ControllerPodLabels) == 0) {
		return errors.New("enabled ingress requires the qualified traefik class, TLS Secret and explicit controller namespace/pod selector")
	}
	return nil
}
func owned(o client.Object, a *api.KeycloakApplication) bool {
	for _, ref := range o.GetOwnerReferences() {
		if ref.UID == a.UID && ref.Kind == "KeycloakApplication" && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}
func (r *Reconciler) credentials(ctx context.Context, ns, name string, database bool) (native.Credentials, error) {
	var c native.Credentials
	if name == "" {
		return c, errors.New("required Secret reference is empty")
	}
	var s corev1.Secret
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		return c, errors.New("referenced Secret is unavailable")
	}
	c.Username = string(s.Data["username"])
	c.Password = string(s.Data["password"])
	c.Database = string(s.Data["database"])
	if c.Username == "" || c.Password == "" || (database && c.Database == "") {
		return c, errors.New("referenced Secret lacks required nonempty keys")
	}
	if c.Username == "postgres" && database {
		return c, errors.New("application database user must not be postgres")
	}
	return c, nil
}
func (r *Reconciler) scale(ctx context.Context, d *appsv1.Deployment, n int32) error {
	if d.Spec.Replicas != nil && *d.Spec.Replicas == n {
		return nil
	}
	before := d.DeepCopy()
	d.Spec.Replicas = &n
	return r.Patch(ctx, d, client.MergeFrom(before))
}
func (r *Reconciler) apply(ctx context.Context, a *api.KeycloakApplication, o client.Object, mutate func() error) error {
	err := r.Reader.Get(ctx, client.ObjectKeyFromObject(o), o)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && !owned(o, a) {
		return fmt.Errorf("%s ownership collision", o.GetName())
	}
	if err := controllerutil.SetControllerReference(a, o, r.Scheme); err != nil {
		return err
	}
	if err := mutate(); err != nil {
		return err
	}
	gvk, err := apiutil.GVKForObject(o, r.Scheme)
	if err != nil {
		return err
	}
	desired, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return err
	}
	desired["apiVersion"], desired["kind"] = gvk.GroupVersion().String(), gvk.Kind
	delete(desired, "status")
	metadata := desired["metadata"].(map[string]interface{})
	for _, field := range []string{"resourceVersion", "uid", "creationTimestamp", "generation", "managedFields", "deletionTimestamp", "deletionGracePeriodSeconds"} {
		delete(metadata, field)
	}
	// Pod annotations are administrator intent (including rollout restart), not
	// fields this controller owns. Server-side apply also retains API defaults
	// without turning every observation into another workload generation.
	unstructured.RemoveNestedField(desired, "spec", "template", "metadata", "annotations")
	payload, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	return r.Patch(ctx, o, client.RawPatch(types.ApplyPatchType, payload), client.FieldOwner("bartl-keycloak"), client.ForceOwnership)
}
func (r *Reconciler) initializing(ctx context.Context, a *api.KeycloakApplication, d *appsv1.Deployment) (bool, bool, error) {
	pods, err := r.applicationPods(ctx, a, d)
	if err != nil {
		return false, false, err
	}
	active := false
	for _, p := range pods {
		if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
			continue
		}
		active = true
		for _, s := range p.Status.ContainerStatuses {
			if s.RestartCount > 0 || s.State.Terminated != nil {
				return true, false, nil
			}
		}
	}
	return active, true, nil
}
func (r *Reconciler) applicationPods(ctx context.Context, a *api.KeycloakApplication, d *appsv1.Deployment) ([]corev1.Pod, error) {
	var sets appsv1.ReplicaSetList
	if err := r.List(ctx, &sets, client.InNamespace(a.Namespace)); err != nil {
		return nil, err
	}
	uids := map[types.UID]bool{}
	for _, s := range sets.Items {
		for _, ref := range s.OwnerReferences {
			if ref.UID == d.UID {
				uids[s.UID] = true
			}
		}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(a.Namespace)); err != nil {
		return nil, err
	}
	result := []corev1.Pod{}
	for _, p := range pods.Items {
		selected := p.Labels[ownerLabel] == string(a.UID) && p.Labels["app.kubernetes.io/name"] == "keycloak"
		for _, ref := range p.OwnerReferences {
			if uids[ref.UID] {
				selected = true
				break
			}
		}
		// Labeled Pods with a missing owner chain block stop/readiness conservatively;
		// labels never authorize adoption or mutation of a foreign Pod.
		if selected {
			result = append(result, p)
		}
	}
	return result, nil
}
func (r *Reconciler) report(ctx context.Context, a *api.KeycloakApplication, class, reason, message string, ready, stop, progress, degraded bool) (ctrl.Result, error) {
	var dep appsv1.Deployment
	var active, replicas int32
	lookupErr := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak"}, &dep)
	if lookupErr != nil && !apierrors.IsNotFound(lookupErr) {
		return ctrl.Result{}, lookupErr
	}
	collision := lookupErr == nil && !owned(&dep, a)
	pods, err := r.applicationPods(ctx, a, &dep)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Terminating and Failed Pods still exist; only actual absence satisfies stopped.
	active = int32(len(pods))
	if lookupErr == nil && !collision {
		replicas = dep.Status.ReadyReplicas
		ready = ready && dep.Status.ObservedGeneration == dep.Generation && dep.Spec.Replicas != nil && *dep.Spec.Replicas == a.Spec.Application.Replicas && replicas == a.Spec.Application.Replicas && active == a.Spec.Application.Replicas
	} else {
		ready = false
	}
	stopped := a.Spec.Application.Replicas == 0 && active == 0 && !collision
	quiescent := !progress && (class == "Valid" || class == "Absent") && (a.Status.Operation == nil || a.Status.Operation.Result == "Complete")
	// Job state reports outstanding work; native locks still enforce exclusion.
	// Manual Jobs also carry the controller's labels on their Pod template.
	if quiescent {
		var jobs batchv1.JobList
		if err := r.List(ctx, &jobs, client.InNamespace(a.Namespace)); err != nil {
			return ctrl.Result{}, err
		}
		for _, job := range jobs.Items {
			if job.Spec.Template.Labels[ownerLabel] != string(a.UID) {
				continue
			}
			finished := false
			for _, condition := range job.Status.Conditions {
				finished = finished || condition.Status == corev1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed)
			}
			if !finished {
				quiescent = false
				break
			}
		}
	}
	a.Status.ObservedGeneration = a.Generation
	a.Status.PackageRelease = a.Spec.PackageRelease
	a.Status.State = api.State{Classification: class, Reason: reason}
	a.Status.Application = api.ApplicationStatus{DesiredReplicas: a.Spec.Application.Replicas, ReadyReplicas: replicas, ActivePods: active}
	values := map[string]bool{"Ready": ready, "Stopped": stopped, "Quiescent": quiescent, "Progressing": progress || (!degraded && !ready && !stop), "Degraded": degraded}
	if len(message) > 700 {
		message = message[:700]
	}
	for _, typ := range []string{"Ready", "Stopped", "Quiescent", "Progressing", "Degraded"} {
		status := metav1.ConditionFalse
		if values[typ] {
			status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{Type: typ, Status: status, ObservedGeneration: a.Generation, Reason: reason, Message: message})
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, err
}

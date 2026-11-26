package controller

import (
	"context"
	"errors"
	"net/url"
	"testing"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func stopFixture(t *testing.T, objects ...client.Object) (*Reconciler, *api.KeycloakApplication) {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	a := &api.KeycloakApplication{ObjectMeta: metav1.ObjectMeta{Name: "keycloak", Namespace: "target", UID: "application", Generation: 4}, Spec: api.Spec{Running: false, Application: api.Application{Replicas: 3}}}
	objects = append(objects, a)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).WithStatusSubresource(&api.KeycloakApplication{}).Build()
	return &Reconciler{Client: c, Reader: c, Scheme: s}, a
}

func stopOwner() []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "KeycloakApplication", Name: "keycloak", UID: "application", Controller: ptr(true)}}
}

func TestStopDrainsManualBackupWithoutAdoptingIt(t *testing.T) {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "target", UID: "manual-job"}, Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{ownerLabel: "application"}}}}}
	db := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "keycloak-db", Namespace: "target", OwnerReferences: stopOwner()}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(1))}}
	r, a := stopFixture(t, job, db)
	if _, err := r.stop(context.Background(), a, &appsv1.Deployment{}, false); err != nil {
		t.Fatal(err)
	}
	var gotDB appsv1.StatefulSet
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(db), &gotDB); err != nil {
		t.Fatal(err)
	}
	if *gotDB.Spec.Replicas != 1 || meta.IsStatusConditionTrue(a.Status.Conditions, "Stopped") {
		t.Fatal("stop shut down the database or claimed completion before manual work drained")
	}
	var gotJob batchv1.Job
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(job), &gotJob); err != nil {
		t.Fatal(err)
	}
	if len(gotJob.OwnerReferences) != 0 || gotJob.Spec.Suspend != nil {
		t.Fatal("manual Job was adopted or suspended using labels as authority")
	}
}

func TestStoppedRequiresActualDatabasePodAbsence(t *testing.T) {
	db := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "keycloak-db", Namespace: "target", UID: "database", OwnerReferences: stopOwner()}, Spec: appsv1.StatefulSetSpec{Replicas: ptr(int32(1))}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "keycloak-db-0", Namespace: "target", Labels: map[string]string{ownerLabel: "application", "app.kubernetes.io/name": "keycloak-db"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	r, a := stopFixture(t, db, pod)
	if _, err := r.stop(context.Background(), a, &appsv1.Deployment{}, false); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(a.Status.Conditions, "Stopped") {
		t.Fatal("database process still exists")
	}
	if err := r.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stop(context.Background(), a, &appsv1.Deployment{}, false); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(a.Status.Conditions, "Stopped") || a.Spec.Application.Replicas != 3 {
		t.Fatal("drained stop did not complete or erased selected serving replica count")
	}
}

func TestStopRetainsInterruptedStartupFailure(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "keycloak", Namespace: "target", OwnerReferences: stopOwner(), Annotations: map[string]string{initializationAnnotation: "true", bindingAnnotation: "bartl-keycloak:::application:storage"}}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1))}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "keycloak-db", Namespace: "target", UID: "storage", Labels: map[string]string{ownerLabel: "application"}, Annotations: map[string]string{bindingAnnotation: ":::"}}}
	r, a := stopFixture(t, dep, pvc)
	if _, err := r.stop(context.Background(), a, dep, true); err != nil {
		t.Fatal(err)
	}
	var got corev1.PersistentVolumeClaim
	if err := r.Get(context.Background(), types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[startupFailureAnnotation] == "" || !meta.IsStatusConditionTrue(a.Status.Conditions, "Degraded") {
		t.Fatal("stop lost the failed native transition boundary")
	}
}

func TestServingZeroIsNotWholeApplicationStopped(t *testing.T) {
	r, a := stopFixture(t)
	a.Spec.Running = true
	a.Spec.Application.Replicas = 0
	if _, err := r.report(context.Background(), a, "Valid", "ServingZero", "Serving only is stopped", false, true, false, false); err != nil {
		t.Fatal(err)
	}
	if meta.IsStatusConditionTrue(a.Status.Conditions, "Stopped") {
		t.Fatal("serving zero was reported as whole-application Stopped")
	}
}

func TestAuthorityRejectsStaleCompletionAndStoppedIntent(t *testing.T) {
	r, a := stopFixture(t)
	stale := a.DeepCopyObject().(*api.KeycloakApplication)
	stale.Spec.Running = true
	if err := r.authority(context.Background(), stale); err == nil {
		t.Fatal("old running intent authorized execution after stop")
	}
	stale.Generation--
	if err := r.authority(context.Background(), stale); err == nil {
		t.Fatal("stale generation authorized execution")
	}
}

// The fake client does not implement server-side apply. Preserve the desired
// typed object and normal fake-client status behavior while exercising the
// controller's resource reads, authority checks and error boundaries.
type managementClient struct {
	client.Client
	middlewareErr error
	backupErr     error
	removeErr     error
}

func (c *managementClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if obj.GetObjectKind().GroupVersionKind().Kind == "Middleware" && c.middlewareErr != nil {
		return c.middlewareErr
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *managementClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if _, ok := obj.(*batchv1.CronJob); ok && c.backupErr != nil {
		return c.backupErr
	}
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	existing := obj.DeepCopyObject().(client.Object)
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return c.Client.Create(ctx, obj)
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	return c.Client.Update(ctx, obj)
}

func (c *managementClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if _, ok := obj.(*batchv1.CronJob); ok && c.removeErr != nil {
		return c.removeErr
	}
	return c.Client.Delete(ctx, obj, opts...)
}

func admittedFixture(t *testing.T) (*Reconciler, *api.KeycloakApplication, *managementClient) {
	t.Helper()
	r, a := stopFixture(t)
	ctx := context.Background()
	a.Spec.Running = true
	a.Spec.Application.Replicas = 1
	a.Spec.Identity = api.Identity{Installation: "installation", Instance: "instance"}
	a.Spec.PackageRelease = "test"
	a.Spec.Configuration.Hostname = "https://login.example.test"
	a.Spec.Database = api.Database{SecretName: "database", AdminSecretName: "database-admin", Size: "1Gi"}
	a.Spec.Ingress = api.Ingress{Enabled: true, ClassName: "traefik", TLSSecretName: "tls", ControllerNamespace: "ingress", ControllerPodLabels: map[string]string{"app": "traefik"}}
	a.Spec.Backup = &api.Backup{Repository: "rest:https://backup.example.test/repository", ReaderSecret: "reader", WriterSecret: "writer", Schedule: "0 * * * *", TimeZone: "UTC"}
	if err := r.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	c := &managementClient{Client: r.Client}
	r.Client, r.Reader = c, c
	r.Namespace, r.Name = a.Namespace, a.Name
	r.KeycloakImage, r.PostgresImage, r.ControllerImage, r.BackupImage = "keycloak:test", "postgres:test", "controller:test", "backup:test"
	pvc, err := r.storage(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	pvc.Annotations[establishedAnnotation] = r.KeycloakImage
	pvc.Annotations["keycloak.bartl.sh/database"] = "keycloak\nkeycloak"
	if err := r.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	binding := "bartl-keycloak:installation:instance:application:" + string(pvc.UID)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database", Namespace: a.Namespace}, Data: map[string][]byte{"username": []byte("keycloak"), "password": []byte("test"), "database": []byte("keycloak")}}
	if err := r.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	for _, reconcile := range []func() error{
		func() error { return r.application(ctx, a, binding, false, false) },
		func() error { return r.routing(ctx, a) },
		func() error { return r.backup(ctx, a, pvc, binding) },
	} {
		if err := reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(a), dep); err != nil {
		t.Fatal(err)
	}
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: dep.Generation, ReadyReplicas: 1, Replicas: 1}
	if err := r.Status().Update(ctx, dep); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "keycloak-running", Namespace: a.Namespace, Labels: labels(a, "keycloak")}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if err := r.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.report(ctx, a, "Valid", "NativeStartupAvailable", "Admitted workload", true, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := r.Status().Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(a.Status.Conditions, "Ready") {
		t.Fatal("fixture did not establish an admitted Ready application")
	}
	return r, a, c
}

func reconcileStatus(t *testing.T, r *Reconciler, a *api.KeycloakApplication) *api.KeycloakApplication {
	t.Helper()
	if err := r.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(a)}); err != nil {
		t.Fatal(err)
	}
	got := &api.KeycloakApplication{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertAdmittedOperation(t *testing.T, r *Reconciler, a *api.KeycloakApplication) {
	t.Helper()
	dep := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), dep); err != nil {
		t.Fatal(err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
		t.Fatal("management failure withdrew admitted application execution")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); err != nil {
		t.Fatalf("management failure withdrew admitted ingress: %v", err)
	}
}

func assertCurrentDegradation(t *testing.T, a *api.KeycloakApplication, reason string) {
	t.Helper()
	for typ, status := range map[string]metav1.ConditionStatus{"Ready": metav1.ConditionFalse, "Degraded": metav1.ConditionTrue} {
		condition := meta.FindStatusCondition(a.Status.Conditions, typ)
		if condition == nil || condition.Status != status || condition.ObservedGeneration != a.Generation || condition.Reason != reason {
			t.Fatalf("%s does not describe the failed current generation: %+v", typ, condition)
		}
	}
}

func TestMaintenanceReadFailurePreservesAdmittedOperation(t *testing.T) {
	for name, failure := range map[string]error{
		"forbidden": apierrors.NewForbidden(schema.GroupResource{Group: "traefik.io", Resource: "middlewares"}, "keycloak-maintenance", errors.New("denied")),
		"timeout":   apierrors.NewTimeoutError("middleware read timed out", 1),
		"transport": &url.Error{Op: "Get", URL: "https://api.example.test", Err: errors.New("connection reset")},
	} {
		t.Run(name, func(t *testing.T) {
			r, a, c := admittedFixture(t)
			a.Generation++
			a.Spec.Ingress.Maintenance = true
			a.Spec.Ingress.AdministratorSourceRanges = []string{"192.0.2.0/24"}
			c.middlewareErr = failure
			got := reconcileStatus(t, r, a)
			assertAdmittedOperation(t, r, a)
			assertCurrentDegradation(t, got, "ObservationUnavailable")
			if err := r.routing(context.Background(), a); !errors.Is(err, failure) {
				t.Fatalf("middleware read lost its cause: %v", err)
			}
		})
	}
}

func TestInvalidMaintenanceConfigurationWithdrawsOperation(t *testing.T) {
	for _, ranges := range [][]string{nil, {"not-a-cidr"}} {
		r, a, _ := admittedFixture(t)
		a.Generation++
		a.Spec.Ingress.Maintenance = true
		a.Spec.Ingress.AdministratorSourceRanges = ranges
		got := reconcileStatus(t, r, a)
		assertCurrentDegradation(t, got, "RoutingRefused")
		dep := &appsv1.Deployment{}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), dep); err != nil {
			t.Fatal(err)
		}
		if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
			t.Fatal("confirmed invalid maintenance configuration did not refuse execution")
		}
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
			t.Fatalf("confirmed invalid maintenance configuration retained ingress: %v", err)
		}
	}
}

func TestBackupConfigurationFailurePreservesAdmittedOperation(t *testing.T) {
	for _, action := range []string{"update", "remove", "invalid", "capture-storage"} {
		t.Run(action, func(t *testing.T) {
			r, a, c := admittedFixture(t)
			a.Generation++
			failure := apierrors.NewForbidden(schema.GroupResource{Group: "batch", Resource: "cronjobs"}, "keycloak-backup", errors.New("denied"))
			switch action {
			case "update":
				a.Spec.Backup.Schedule = "30 * * * *"
				c.backupErr = failure
			case "remove":
				a.Spec.Backup = nil
				c.removeErr = failure
			case "invalid":
				a.Spec.Backup.Repository = "http://invalid.example.test"
			case "capture-storage":
				pvc := &corev1.PersistentVolumeClaim{}
				if err := r.Get(context.Background(), types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-capture"}, pvc); err != nil {
					t.Fatal(err)
				}
				pvc.Spec.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("2Gi")
				if err := r.Update(context.Background(), pvc); err != nil {
					t.Fatal(err)
				}
			}
			got := reconcileStatus(t, r, a)
			assertAdmittedOperation(t, r, a)
			assertCurrentDegradation(t, got, "BackupConfigurationRefused")
			cron := &batchv1.CronJob{}
			if err := r.Get(context.Background(), types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-backup"}, cron); err != nil {
				t.Fatal(err)
			}
			if cron.Spec.Schedule != "0 * * * *" {
				t.Fatal("failed configuration mutation unexpectedly changed the schedule")
			}
			c.backupErr, c.removeErr = nil, nil
			if action == "invalid" || action == "capture-storage" {
				return
			}
			got = reconcileStatus(t, r, got)
			if !meta.IsStatusConditionTrue(got.Status.Conditions, "Ready") || meta.IsStatusConditionTrue(got.Status.Conditions, "Degraded") {
				t.Fatal("successful backup reconciliation did not restore accurate readiness")
			}
		})
	}
}

func TestBackupConfigurationFailurePreservesStartupRefusal(t *testing.T) {
	r, a, c := admittedFixture(t)
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Annotations[startupFailureAnnotation] = "Exclusive startup was interrupted"
	if err := r.Update(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	a.Generation++
	a.Spec.Backup.Schedule = "30 * * * *"
	c.backupErr = errors.New("backup update failed")
	got := reconcileStatus(t, r, a)
	assertCurrentDegradation(t, got, "StartupInterrupted")
	dep := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), dep); err != nil {
		t.Fatal(err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		t.Fatal("backup failure bypassed persisted startup refusal")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Fatalf("backup failure bypassed startup ingress withdrawal: %v", err)
	}
}

func TestBackupConfigurationFailurePreservesRuntimeChanges(t *testing.T) {
	r, a, c := admittedFixture(t)
	a.Generation++
	a.Spec.Application.Replicas = 0
	a.Spec.Ingress.Enabled = false
	a.Spec.Backup = nil
	c.removeErr = errors.New("backup removal failed")
	got := reconcileStatus(t, r, a)
	assertCurrentDegradation(t, got, "BackupConfigurationRefused")
	dep := &appsv1.Deployment{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), dep); err != nil {
		t.Fatal(err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		t.Fatal("backup failure blocked serving scale intent")
	}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(a), &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Fatalf("backup failure blocked ingress intent: %v", err)
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, "Stopped") {
		t.Fatal("backup failure or serving-zero intent became whole-application Stopped")
	}
}

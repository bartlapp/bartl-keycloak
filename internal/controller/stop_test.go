package controller

import (
	"context"
	"testing"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
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

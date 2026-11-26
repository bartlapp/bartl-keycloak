package controller

import (
	"context"
	"errors"
	"fmt"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Re-read intent immediately before admitting resources. A cached completion or
// an earlier reconcile must not grant new running authority after stop.
func (r *Reconciler) authority(ctx context.Context, a *api.KeycloakApplication) error {
	latest := new(api.KeycloakApplication)
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
		return err
	}
	if latest.UID != a.UID || latest.Generation != a.Generation || !latest.Spec.Running || !latest.DeletionTimestamp.IsZero() {
		return errors.New("running authority changed; no new work admitted")
	}
	return nil
}

func jobFinished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
			return true
		}
	}
	return false
}

// CronJob-created manual Jobs need not have an owner reference. Their inherited
// labels identify outstanding work for observation only, never authorize mutation.
func (r *Reconciler) backgroundDrained(ctx context.Context, a *api.KeycloakApplication) (bool, error) {
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(a.Namespace)); err != nil {
		return false, err
	}
	uids := map[types.UID]bool{}
	drained := true
	for _, j := range jobs.Items {
		if !owned(&j, a) && j.Spec.Template.Labels[ownerLabel] != string(a.UID) {
			continue
		}
		uids[j.UID] = true
		if !jobFinished(&j) {
			drained = false
		}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(a.Namespace)); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		selected := p.Labels[ownerLabel] == string(a.UID) && p.Labels["app.kubernetes.io/name"] == "keycloak-backup"
		for _, ref := range p.OwnerReferences {
			selected = selected || uids[ref.UID]
		}
		if selected && (!p.DeletionTimestamp.IsZero() || p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed) {
			drained = false
		}
	}
	return drained, nil
}

func (r *Reconciler) databaseStopped(ctx context.Context, a *api.KeycloakApplication) (bool, error) {
	var db appsv1.StatefulSet
	err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, &db)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if err == nil && (!owned(&db, a) || db.Spec.Replicas == nil || *db.Spec.Replicas != 0 || db.Status.Replicas != 0) {
		return false, nil
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(a.Namespace)); err != nil {
		return false, err
	}
	for _, p := range pods.Items {
		if p.Labels[ownerLabel] == string(a.UID) && p.Labels["app.kubernetes.io/name"] == "keycloak-db" {
			return false, nil
		}
		for _, ref := range p.OwnerReferences {
			if db.UID != "" && ref.UID == db.UID {
				return false, nil
			}
		}
	}
	return true, nil
}

func (r *Reconciler) stoppedComponents(ctx context.Context, a *api.KeycloakApplication) (bool, error) {
	var c batchv1.CronJob
	if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-backup"}, &c); err == nil {
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	drained, err := r.backgroundDrained(ctx, a)
	if err != nil || !drained {
		return false, err
	}
	return r.databaseStopped(ctx, a)
}

func (r *Reconciler) stop(ctx context.Context, a *api.KeycloakApplication, dep *appsv1.Deployment, exists bool) (ctrl.Result, error) {
	if err := r.removeIngress(ctx, a); err != nil {
		return ctrl.Result{}, err
	}
	// Removing the trigger also prevents native manual admission from its template.
	// Orphan propagation retains already admitted Jobs and their diagnostic evidence.
	if err := r.removeBackup(ctx, a); err != nil {
		return ctrl.Result{}, err
	}
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, &pvc)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	pvcBinding := a.Spec.Identity.Installation + ":" + a.Spec.Identity.Instance + ":" + a.Spec.Database.SecretName + ":" + a.Spec.Database.AdminSecretName
	targetBinding := fmt.Sprintf("bartl-keycloak:%s:%s:%s:%s", a.Spec.Identity.Installation, a.Spec.Identity.Instance, a.UID, pvc.UID)
	if exists && dep.Annotations[initializationAnnotation] == "true" && pvc.Labels[ownerLabel] == string(a.UID) && pvc.Annotations[bindingAnnotation] == pvcBinding && dep.Annotations[bindingAnnotation] == targetBinding && pvc.Annotations[establishedAnnotation] == "" && pvc.Annotations[startupFailureAnnotation] == "" {
		if err := r.annotateRecovery(ctx, &pvc, startupFailureAnnotation, "Exclusive native startup interrupted by whole-application stop; replace the failed target before retry"); err != nil {
			return ctrl.Result{}, err
		}
	}
	if exists {
		if err := r.scale(ctx, dep, 0); err != nil {
			return ctrl.Result{}, err
		}
	}
	pods, err := r.applicationPods(ctx, a, dep)
	if err != nil {
		return ctrl.Result{}, err
	}
	drained, err := r.backgroundDrained(ctx, a)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(pods) == 0 && drained {
		var db appsv1.StatefulSet
		err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-db"}, &db)
		if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		if err == nil {
			if !owned(&db, a) {
				return r.report(ctx, a, "Unknown", "OwnershipCollision", "Database is not owned by this application; stop is unconfirmed", false, true, false, true)
			}
			if db.Spec.Replicas == nil || *db.Spec.Replicas != 0 {
				before := db.DeepCopy()
				db.Spec.Replicas = ptr(int32(0))
				if err := r.Patch(ctx, &db, client.MergeFrom(before)); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}
	class, reason := a.Status.State.Classification, "ApplicationStopping"
	if class == "" {
		class = "Unknown"
	}
	degraded := pvc.Annotations[startupFailureAnnotation] != ""
	if degraded {
		class = "Incomplete"
		if a.Status.Operation != nil && a.Status.Operation.Result != "Complete" {
			a.Status.Operation.Result = "Failed"
		}
	}
	stopped, err := r.stoppedComponents(ctx, a)
	if err != nil {
		return ctrl.Result{}, err
	}
	if stopped && len(pods) == 0 {
		reason = "ApplicationStopped"
	}
	message := "Whole-application stop retains data; waiting for owned work and components to end"
	if failure := pvc.Annotations[startupFailureAnnotation]; failure != "" {
		message += "; " + failure
	}
	return r.report(ctx, a, class, reason, message, false, true, false, degraded)
}

package controller

import (
	"context"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha1"
	"github.com/floriankesslergmbh/bartl-keycloak/internal/native"
	"github.com/jackc/pgx/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const upgradeAnnotation = "keycloak.bartl.sh/upgrade"
const upgradePair = native.UpgradeSourceVersion + ":" + native.Version

func (r *Reconciler) upgrade(ctx context.Context, a *api.KeycloakApplication, pvc *corev1.PersistentVolumeClaim, binding string, conn *pgx.Conn, o native.Observation, dep *appsv1.Deployment, exists bool) (bool, ctrl.Result, error) {
	pending := o.Classification == "Upgradeable" || o.Classification == "Upgrading"
	if !pending && (!exists || dep.Annotations[upgradeAnnotation] == "") {
		return false, ctrl.Result{}, nil
	}
	fail := func(reason, message string) (bool, ctrl.Result, error) {
		if err := r.annotateRecovery(ctx, pvc, startupFailureAnnotation, reason+": "+message); err != nil {
			return true, ctrl.Result{}, err
		}
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
		}
		if err := r.removeIngress(ctx, a); err != nil {
			return true, ctrl.Result{}, err
		}
		result, err := r.report(ctx, a, o.Classification, reason, message, false, false, false, true)
		return true, result, err
	}
	if r.KeycloakImage != native.KeycloakImage {
		return fail("UpgradeImageRefused", "Native version transition requires the package's exact target image")
	}
	if o.Classification == "Valid" {
		// Completion is durable. Ordinary reconciliation removes exclusive admission.
		return false, ctrl.Result{}, nil
	}
	if err := r.removeIngress(ctx, a); err != nil {
		return true, ctrl.Result{}, err
	}
	if a.Spec.Application.Replicas == 0 {
		result, err := r.report(ctx, a, o.Classification, "UpgradeStopped", "Persistent stop retained; native migration does not authorize startup", false, true, false, true)
		return true, result, err
	}
	if exists && len(dep.Spec.Template.Spec.Containers) > 0 && dep.Spec.Template.Spec.Containers[0].Image != native.UpgradeSourceImage && dep.Spec.Template.Spec.Containers[0].Image != native.KeycloakImage {
		return fail("UpgradeImageRefused", "Existing workload image is outside the supported native pair")
	}
	admitted := exists && dep.Annotations[upgradeAnnotation] == upgradePair && len(dep.Spec.Template.Spec.Containers) == 1 && dep.Spec.Template.Spec.Containers[0].Image == r.KeycloakImage
	if !admitted {
		if o.Reason == "NativeUpgradeStarted" {
			return fail("UpgradeInterrupted", "Admitted native migration has no surviving target workload; refusing replay")
		}
		if exists {
			if err := r.scale(ctx, dep, 0); err != nil {
				return true, ctrl.Result{}, err
			}
			pods, err := r.applicationPods(ctx, a, dep)
			if err != nil {
				return true, ctrl.Result{}, err
			}
			if len(pods) != 0 {
				result, err := r.report(ctx, a, o.Classification, "UpgradeDraining", "Waiting for every old Keycloak Pod to disappear before native migration", false, false, true, false)
				return true, result, err
			}
		}
		if o.Classification != "Upgradeable" && o.Classification != "Upgrading" {
			return fail("UpgradeAdmissionRefused", "Native upgrade state has no matching exclusive target workload")
		}
		if o.Classification == "Upgradeable" {
			if err := native.AdmitUpgrade(ctx, conn, binding); err != nil {
				return fail("UpgradeAdmissionRefused", err.Error())
			}
		}
	} else {
		active, healthy, err := r.initializing(ctx, a, dep)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if o.Classification == "Upgrading" && o.Reason == "NativeUpgradeStarted" && active && healthy && dep.Status.ObservedGeneration == dep.Generation && dep.Status.ReadyReplicas == 1 {
			if err := native.CompleteUpgrade(ctx, conn, binding); err != nil {
				return fail("UpgradeVerificationRefused", err.Error())
			}
			result, err := r.report(ctx, a, "Valid", "NativeUpgradeVerified", "Native target version and exclusive startup readiness verified; no restore was performed", false, false, true, false)
			return true, result, err
		}
		if !healthy {
			return fail("UpgradeInterrupted", "Native migration did not establish a complete target; refusing replay and retaining isolation")
		}
		if active {
			result, err := r.report(ctx, a, o.Classification, "NativeUpgradeExecuting", "One exclusive native Keycloak startup is executing; public access remains withheld", false, false, true, false)
			return true, result, err
		}
		if o.Classification == "Incomplete" || o.Reason == "NativeUpgradeStarted" {
			return fail("UpgradeInterrupted", "Native migration has no complete target result and no surviving process; refusing replay")
		}
	}
	latest := new(api.KeycloakApplication)
	if err := r.Reader.Get(ctx, client.ObjectKeyFromObject(a), latest); err != nil {
		return true, ctrl.Result{}, err
	}
	if latest.Generation != a.Generation || latest.Spec.Application.Replicas == 0 {
		return true, ctrl.Result{Requeue: true}, nil
	}
	if err := r.application(ctx, a, binding, false, true, true); err != nil {
		return fail("UpgradeWorkloadRefused", err.Error())
	}
	result, err := r.report(ctx, a, o.Classification, "NativeUpgradeAdmitted", "One exclusive target startup is admitted to run Keycloak's native migration", false, false, true, false)
	return true, result, err
}

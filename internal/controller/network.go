package controller

import (
	"context"
	"errors"
	"net"
	"net/url"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *Reconciler) policies(ctx context.Context, a *api.KeycloakApplication) error {
	tcp := corev1.ProtocolTCP
	udp := corev1.ProtocolUDP
	port := func(n int32) intstr.IntOrString { return intstr.FromInt32(n) }
	peer := func(component string) networkingv1.NetworkPolicyPeer {
		return networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: labels(a, component)}}
	}
	appPeer := peer("keycloak")
	dbPeer := peer("keycloak-db")
	controllerPeer := networkingv1.NetworkPolicyPeer{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "keycloak-controller"}}}
	database := &networkingv1.NetworkPolicy{ObjectMeta: metadata(a, "keycloak-db")}
	if err := r.apply(ctx, a, database, func() error {
		database.Spec = networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels(a, "keycloak-db")}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{appPeer, controllerPeer, peer("keycloak-backup")}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(5432))}}}}, Egress: []networkingv1.NetworkPolicyEgressRule{}}
		return nil
	}); err != nil {
		return err
	}
	// Keep this rule when configuration is removed: already admitted Jobs retain
	// their immutable template and must be allowed to finish.
	backup := &networkingv1.NetworkPolicy{ObjectMeta: metadata(a, "keycloak-backup")}
	if err := r.apply(ctx, a, backup, func() error {
		dns := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}
		backup.Spec = networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels(a, "keycloak-backup")}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{}, Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{dbPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(5432))}}},
			{To: []networkingv1.NetworkPolicyPeer{dns}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: ptr(port(53))}, {Protocol: &tcp, Port: ptr(port(53))}}},
			// Repository URLs may use arbitrary HTTPS ports. Keep TCP egress
			// independent of current intent so old immutable Jobs can finish.
			{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp}}},
		}}
		return nil
	}); err != nil {
		return err
	}
	application := &networkingv1.NetworkPolicy{ObjectMeta: metadata(a, "keycloak")}
	return r.apply(ctx, a, application, func() error {
		ingress := []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{appPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(7800))}, {Protocol: &tcp, Port: ptr(port(57800))}}}}
		if a.Spec.Ingress.Enabled {
			source := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": a.Spec.Ingress.ControllerNamespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: a.Spec.Ingress.ControllerPodLabels}}
			ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{From: []networkingv1.NetworkPolicyPeer{source}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(8080))}}})
		}
		dns := networkingv1.NetworkPolicyPeer{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}
		application.Spec = networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels(a, "keycloak")}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: ingress, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{dbPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(5432))}}}, {To: []networkingv1.NetworkPolicyPeer{appPeer}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr(port(7800))}, {Protocol: &tcp, Port: ptr(port(57800))}}}, {To: []networkingv1.NetworkPolicyPeer{dns}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: ptr(port(53))}, {Protocol: &tcp, Port: ptr(port(53))}}}}}
		return nil
	})
}
func (r *Reconciler) routing(ctx context.Context, a *api.KeycloakApplication) error {
	if !a.Spec.Ingress.Enabled {
		return r.removeIngress(ctx, a)
	}
	annotations := map[string]string{}
	if a.Spec.Ingress.Maintenance {
		if len(a.Spec.Ingress.AdministratorSourceRanges) == 0 {
			return errors.New("maintenance requires explicit administrator CIDRs")
		}
		ranges := make([]interface{}, 0, len(a.Spec.Ingress.AdministratorSourceRanges))
		for _, cidr := range a.Spec.Ingress.AdministratorSourceRanges {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				return errors.New("invalid administrator source CIDR")
			}
			ranges = append(ranges, cidr)
		}
		middleware := &unstructured.Unstructured{}
		middleware.SetAPIVersion("traefik.io/v1alpha1")
		middleware.SetKind("Middleware")
		key := types.NamespacedName{Namespace: a.Namespace, Name: "keycloak-maintenance"}
		err := r.Reader.Get(ctx, key, middleware)
		absent := apierrors.IsNotFound(err)
		if err != nil && !absent {
			return errors.New("maintenance requires the installed Traefik Middleware CRD and reader permission")
		}
		if absent {
			middleware.SetNamespace(a.Namespace)
			middleware.SetName(key.Name)
		} else if !owned(middleware, a) {
			return errors.New("maintenance Middleware ownership collision")
		}
		if err := controllerutil.SetControllerReference(a, middleware, r.Scheme); err != nil {
			return err
		}
		middleware.Object["spec"] = map[string]interface{}{"ipAllowList": map[string]interface{}{"sourceRange": ranges}}
		if absent {
			err = r.Create(ctx, middleware)
		} else {
			err = r.Update(ctx, middleware)
		}
		if err != nil {
			return err
		}
		annotations["traefik.ingress.kubernetes.io/router.middlewares"] = a.Namespace + "-keycloak-maintenance@kubernetescrd"
	}
	u, _ := url.Parse(a.Spec.Configuration.Hostname)
	ingress := &networkingv1.Ingress{ObjectMeta: metadata(a, "keycloak")}
	return r.apply(ctx, a, ingress, func() error {
		ingress.Annotations = annotations
		ingress.Spec = networkingv1.IngressSpec{IngressClassName: ptr(a.Spec.Ingress.ClassName), TLS: []networkingv1.IngressTLS{{Hosts: []string{u.Hostname()}, SecretName: a.Spec.Ingress.TLSSecretName}}, Rules: []networkingv1.IngressRule{{Host: u.Hostname(), IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: ptr(networkingv1.PathTypePrefix), Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "keycloak", Port: networkingv1.ServiceBackendPort{Number: 8080}}}}}}}}}}
		return nil
	})
}
func (r *Reconciler) removeIngress(ctx context.Context, a *api.KeycloakApplication) error {
	ingress := &networkingv1.Ingress{}
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: "keycloak"}, ingress); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !owned(ingress, a) {
		return errors.New("Ingress ownership collision")
	}
	return client.IgnoreNotFound(r.Delete(ctx, ingress))
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	api "github.com/floriankesslergmbh/bartl-keycloak/api/v1alpha1"
	"github.com/floriankesslergmbh/bartl-keycloak/internal/controller"
	"github.com/floriankesslergmbh/bartl-keycloak/internal/native"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	ctrl.SetLogger(klogr.New())
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "keycloak-start":
			return native.KeycloakStart()
		case "install":
			src, err := os.Open("/controller")
			if err != nil {
				return err
			}
			defer src.Close()
			dst, err := os.OpenFile("/shared/controller", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
			if err != nil {
				return err
			}
			_, err = io.Copy(dst, src)
			closeErr := dst.Close()
			if err != nil {
				return err
			}
			return closeErr
		}
	}
	var health, metrics string
	flag.StringVar(&health, "health-probe-bind-address", ":8081", "Probe address")
	flag.StringVar(&metrics, "metrics-bind-address", "0", "Metrics address")
	flag.Parse()
	ns := os.Getenv("WATCH_NAMESPACE")
	name := os.Getenv("APPLICATION_NAME")
	if ns == "" || name != "keycloak" {
		return fmt.Errorf("WATCH_NAMESPACE and APPLICATION_NAME=keycloak are required")
	}
	for _, key := range []string{"KEYCLOAK_IMAGE", "POSTGRES_IMAGE", "CONTROLLER_IMAGE", "BACKUP_IMAGE"} {
		if os.Getenv(key) == "" {
			return fmt.Errorf("%s is required", key)
		}
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}, Metrics: metricsserver.Options{BindAddress: metrics}, HealthProbeBindAddress: health, LeaderElection: true, LeaderElectionNamespace: ns, LeaderElectionID: "keycloak.bartl.sh"})
	if err != nil {
		return err
	}
	// Informers trigger work; lifecycle reads use current API state, not a stale cache.
	direct, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	r := &controller.Reconciler{Client: direct, Reader: mgr.GetAPIReader(), Scheme: scheme, Namespace: ns, Name: name, KeycloakImage: os.Getenv("KEYCLOAK_IMAGE"), PostgresImage: os.Getenv("POSTGRES_IMAGE"), ControllerImage: os.Getenv("CONTROLLER_IMAGE"), BackupImage: os.Getenv("BACKUP_IMAGE")}
	if err := r.Setup(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}

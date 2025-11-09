package main

import (
	"flag"
	mariadbv1alpha1 "github.com/mariadb-operator/mariadb-operator/v25/api/v1alpha1"
	"github.com/spf13/pflag"
	crmv1 "hostzero.de/m/v2/api/v1"
	controller "hostzero.de/m/v2/internal/controller"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	// Add flag for metrics address
	var metricsAddr string
	var healthProbeAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443",
		"The address the metric endpoint binds to.")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081",
		"The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")

	// Set up zap logger options with better defaults for development
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)

	// Set up flags
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	// Use zap logger with development settings
	logger := zap.New(zap.UseFlagOptions(&opts))
	ctrl.SetLogger(logger)

	// Get environment and log it
	environment := os.Getenv("ENVIRONMENT")
	if environment == "" {
		errorMsg := "ENVIRONMENT variable is not set."
		logger.Error(nil, errorMsg)
	}

	TlsClusterIssuer := os.Getenv("TLS_CLUSTER_ISSUER")
	if TlsClusterIssuer == "" {
		errorMsg := "TLS_CLUSTER_ISSUER variable is not set."
		logger.Error(nil, errorMsg)
		os.Exit(1)
	}

	MariaDBReplicas := os.Getenv("MARIADB_REPLICAS")
	if MariaDBReplicas == "" {
		errorMsg := "MARIADB_REPLICAS variable is not set."
		logger.Error(nil, errorMsg)
		os.Exit(1)
	}

	StorageClassName := os.Getenv("STORAGE_CLASS_NAME")
	if StorageClassName == "" {
		logger.Info("STORAGE_CLASS_NAME variable is not set. Default storage class will be used.")
	}

	CiliumSharingKey := os.Getenv("CILIUM_SHARING_KEY")
	if CiliumSharingKey == "" {
		logger.Info("CILIUM_SHARING_KEY variable is not set. No Annotation on services will be set.")
	}

	PHPMyAdminDomain := os.Getenv("PHPMYADMIN_DOMAIN")
	if PHPMyAdminDomain == "" {
		logger.Info("PHPMYADMIN_DOMAIN variable is not set. This needs to be set to enable phpMyAdmin access.")
		os.Exit(1)
	}

	// log app version
	version := os.Getenv("VERSION")

	logger.Info("Starting WordPress operator",
		"version", version,
		"metrics-addr", metricsAddr,
		"health-addr", healthProbeAddr,
		"leader-election", enableLeaderElection,
		"environment", environment,
		"tls-cluster-issuer", TlsClusterIssuer,
		"mariadb-replicas", MariaDBReplicas,
		"storage-class-name", StorageClassName)

	// Create manager with health probe
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: healthProbeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kubepress-leader-election",
	})
	if err != nil {
		logger.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	// Add health check endpoints
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "Unable to set up health check")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	// Add the APIs to the scheme
	if err := crmv1.AddToScheme(mgr.GetScheme()); err != nil {
		logger.Error(err, "Unable to add APIs to scheme")
		os.Exit(1)
	}

	// Add the MariaDB operator APIs to the scheme
	if err := mariadbv1alpha1.AddToScheme(mgr.GetScheme()); err != nil {
		logger.Error(err, "Unable to add MariaDB APIs to scheme")
		os.Exit(1)
	}

	// Register the WordPressSiteReconciler with the manager
	if err := (&controller.WordPressSiteReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("wordpresssite-controller"),
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "Unable to create controller", "controller", "WordPressSite")
		os.Exit(1)
	}

	// Start the manager
	logger.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "Problem running manager")
		os.Exit(1)
	}
}

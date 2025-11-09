package controller

import (
	"context"
	"database/sql"
	"fmt"
	mariadbv1alpha1 "github.com/mariadb-operator/mariadb-operator/v25/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	crmv1 "hostzero.de/m/v2/api/v1"
	"hostzero.de/m/v2/internal/controller/wordpress"
)

// WordPressSite resources
//+kubebuilder:rbac:groups=crm.hostzero.de,resources=wordpresssites,verbs=get;list;watch;create;update;patch;delete

// WordPressSite status resources
//+kubebuilder:rbac:groups=crm.hostzero.de,resources=wordpresssites/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=crm.hostzero.de,resources=wordpresssites/finalizers,verbs=update

// Core API resources
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups=core,resources=configmaps;deployments;persistentvolumeclaims;persistentvolumes;pods;secrets;services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch;update

// Apps API resources
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete

// Storage API resources
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;patch;delete

// Job API resources (for Central PV cleanup)
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete

// MariaDBDatabase resources (for managing the database)
//+kubebuilder:rbac:groups=k8s.mariadb.com,resources=mariadbs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k8s.mariadb.com,resources=databases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k8s.mariadb.com,resources=users,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k8s.mariadb.com,resources=grants,verbs=get;list;watch;create;update;patch;delete

const (
	wordpressFinalizer = "crm.hostzero.de/finalizer"
)

const (
	StatusUnknown                   = "Unknown"                   // Initial status, not set yet
	StatusValidationFailed          = "ValidationFailed"          // Validation failed, e.g. the domain is already in use
	StatusContainerReady            = "ContainerReady"            // Container is ready, first status to set
	StatusWordPressReady            = "WordPressReady"            // db is ready & wp tables exist & container is running
	StatusWordPressReadyAndDeployed = "WordPressReadyAndDeployed" // WordPress is ready and ingress is deployed
	StatusTerminating               = "Terminating"               // Status when the resource is being deleted
)

type WordPressSiteReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// Reconcile handles the creation and management of WordPress site resources
func (r *WordPressSiteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the WordPressSite instance
	wp := &crmv1.WordPressSite{}
	if err := r.Get(ctx, req.NamespacedName, wp); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			logger.Info("WordPressSite resource not found. Ignoring since object must be deleted")
			// Return and don't requeue
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get WordPressSite")
		return ctrl.Result{}, err
	}

	// Handle deletion with finalizer
	if !wp.ObjectMeta.DeletionTimestamp.IsZero() {
		logger.V(1).Info("Deleting WordPressSite", "name", wp.Name)

		// instantly set the status to terminating
		if wp.Status.DeploymentStatus != StatusTerminating {
			wp.Status.DeploymentStatus = StatusTerminating
			if err := r.Status().Update(ctx, wp); err != nil {
				logger.Error(err, "Failed to update WordPressSite status to Terminating")
				return ctrl.Result{}, err
			}
		}

		if wordpress.ContainsString(wp.ObjectMeta.Finalizers, wordpressFinalizer) {
			// Remove finalizer from the list and update it
			// Do any cleanup logic here if needed and remove finalizer when done
			wp.ObjectMeta.Finalizers = wordpress.RemoveString(wp.ObjectMeta.Finalizers, wordpressFinalizer)
			if err := r.Update(ctx, wp); err != nil {
				return ctrl.Result{}, err
			}
		}
		// Stop reconciliation as the object is being deleted
		return ctrl.Result{}, nil
	}

	logger.V(1).Info("Reconciling WordPressSite", "wordpresssite", req.NamespacedName)

	// Add finalizer for this CR if not present
	if !wordpress.ContainsString(wp.ObjectMeta.Finalizers, wordpressFinalizer) {
		wp.ObjectMeta.Finalizers = append(wp.ObjectMeta.Finalizers, wordpressFinalizer)
		if err := r.Update(ctx, wp); err != nil {
			logger.Error(err, "Failed to add Finalizer to WordPress site")
			return ctrl.Result{}, err
		}
	}

	// Ensure UUID is present as a label
	if wp.Labels == nil {
		wp.Labels = make(map[string]string)
	}
	if wp.Labels["hostzero.com/resource-uid"] != string(wp.GetUID()) {
		wp.Labels["hostzero.com/resource-uid"] = string(wp.GetUID())
		if err := r.Update(ctx, wp); err != nil {
			logger.Error(err, "Failed to update WordPress site with UID label")
			return ctrl.Result{}, err
		}
		// Return here to avoid conflicts with subsequent operations
		return ctrl.Result{Requeue: true}, nil
	}

	// Initialize status if needed
	if wp.Status.Conditions == nil {
		wp.Status.Conditions = []metav1.Condition{}
	}

	// Set initial deployment status if not already set
	if wp.Status.DeploymentStatus == "" {
		wp.Status.DeploymentStatus = StatusUnknown
		if err := r.Status().Update(ctx, wp); err != nil {
			logger.Error(err, "Failed to update initial status")
			return ctrl.Result{}, err
		}
		// Return here to avoid conflicts with subsequent operations
		return ctrl.Result{Requeue: true}, nil
	}

	// check if there is already a resource with the same domain
	if wp.Spec.Ingress.Host != "" {
		existing := &crmv1.WordPressSiteList{}
		if err := r.List(ctx, existing, client.MatchingLabels{"hostzero.com/domain": wp.Spec.Ingress.Host}); err != nil {
			logger.Error(err, "Failed to list WordPress sites with the same host", "host", wp.Spec.Ingress.Host)
			return ctrl.Result{}, err
		}
		if len(existing.Items) > 0 && existing.Items[0].Name != wp.Name {
			logger.Info("Another WordPress site with the same host already exists, requeuing",
				"host", wp.Spec.Ingress.Host, "name", existing.Items[0].Name)

			// Set the status to validation failed
			wp.Status.DeploymentStatus = StatusValidationFailed
			wp.Status.Ready = false

			r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", fmt.Sprintf("Another WordPress site with the same host (%s) already exists", wp.Spec.Ingress.Host))

			if err := r.Status().Update(ctx, wp); err != nil {
				logger.Error(err, "Failed to update WordPressSite status with validation failure")
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Second * 120}, nil
		}
	}

	// check if the pvc is now smaller than before
	pvcName := wordpress.GetPVCName(wp.Name)

	// Check if central PVC exists
	pvc := &v1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: wp.Namespace}, pvc)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get central PVC", "name", pvcName)
		return ctrl.Result{}, err
	}

	// if pvc exists, check if the requested size is smaller than the existing one
	// if so, do not update and log a warning
	if err == nil {
		quantity := pvc.Spec.Resources.Requests[v1.ResourceStorage]
		if quantity.Cmp(resource.MustParse(wp.Spec.WordPress.StorageSize)) == 1 {
			logger.Info("Requested Storage Size is smaller than the current one. It's usually not possible to scale the PVC down. Setting Validation to failed.", "current", quantity, "requested", wp.Spec.WordPress.StorageSize)
			// set a validation error on the WordPress resource
			wp.Status.DeploymentStatus = StatusValidationFailed
			wp.Status.Ready = false

			r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", fmt.Sprintf("Requested Storage Size is smaller than the current one. It's usually not possible to scale the PVC down. Setting Validation to failed. current: %s, requested: %s", quantity.String(), wp.Spec.WordPress.StorageSize))

			if err := r.Status().Update(ctx, wp); err != nil {
				logger.Error(err, "Failed to update WordPressSite status with validation failure")
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Second * 120}, nil
		}
	}

	// check if secret exists, if not requeue
	// the secret needs a username and a password
	secretName := wp.Spec.AdminUserSecretKeyRef
	existingSecret := &v1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: req.Namespace}, existingSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Supplied secret not found, requeuing", "name", secretName, "namespace", wp.Namespace)

			// Set the status to validation failed
			wp.Status.DeploymentStatus = StatusValidationFailed
			wp.Status.Ready = false

			r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", "Supplied secret does not exist")

			if err := r.Status().Update(ctx, wp); err != nil {
				logger.Error(err, "Failed to update WordPressSite status with validation failure")
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Second * 120}, nil
		}
		logger.Error(err, "Failed to get database secret", "name", secretName, "namespace", wp.Namespace)
		return ctrl.Result{}, err
	}

	// check that username and password fields exist
	if _, ok := existingSecret.Data["username"]; !ok {
		logger.Info("User supplied secret is missing 'username' field, requeuing", "name", secretName, "namespace", wp.Namespace)

		// Set the status to validation failed
		wp.Status.DeploymentStatus = StatusValidationFailed
		wp.Status.Ready = false

		r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", "Supplied secret is missing 'username' field")

		if err := r.Status().Update(ctx, wp); err != nil {
			logger.Error(err, "Failed to update WordPressSite status with validation failure")
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Second * 120}, nil
	}

	if _, ok := existingSecret.Data["password"]; !ok {
		logger.Info("User supplied secret is missing 'password' field, requeuing", "name", secretName, "namespace", wp.Namespace)

		// Set the status to validation failed
		wp.Status.DeploymentStatus = StatusValidationFailed
		wp.Status.Ready = false

		r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", "Supplied secret is missing 'password' field")

		if err := r.Status().Update(ctx, wp); err != nil {
			logger.Error(err, "Failed to update WordPressSite status with validation failure")
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: time.Second * 120}, nil
	}

	_, existsDatabaseHost := existingSecret.Data["databaseHost"]
	_, existsDatabase := existingSecret.Data["database"]
	_, existsDatabaseUsername := existingSecret.Data["databaseUsername"]

	if wp.Spec.Database.CreateNew {
		// we create the database
		// there are two cases we need to consider: db has already been created, or not
		if existsDatabaseHost || existsDatabase {
			// if one of those fields exists, it means that we already created the db
			// so the following fields need to exist as well: databaseHost, database, databaseUsername
			if !existsDatabaseHost || !existsDatabase || !existsDatabaseUsername {
				logger.Info("User supplied secret is missing one of the required database fields: 'databaseHost', 'database', 'databaseUsername'. Did you delete any field from the secret? This seams like corrupted data. Requeuing...", "name", secretName, "namespace", wp.Namespace)

				// Set the status to validation failed
				wp.Status.DeploymentStatus = StatusValidationFailed
				wp.Status.Ready = false

				r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", "Supplied secret is missing one of the required database fields: 'databaseHost', 'database', 'databaseUsername'")

				if err := r.Status().Update(ctx, wp); err != nil {
					logger.Error(err, "Failed to update WordPressSite status with validation failure")
					return ctrl.Result{}, err
				}

				return ctrl.Result{RequeueAfter: time.Second * 120}, nil
			}
		}
	} else {
		// we use an existing database
		// the following fields need to exist: databaseHost, database, databaseUsername (in addition to username and password, but this is checked beforehand / above)
		if !existsDatabaseHost || !existsDatabase || !existsDatabaseUsername {
			logger.Info("In case of using an existing database, the supplied secret must contain the following fields: 'databaseHost', 'database', 'databaseUsername'. Requeuing...", "name", secretName, "namespace", wp.Namespace)

			// Set the status to validation failed
			wp.Status.DeploymentStatus = StatusValidationFailed
			wp.Status.Ready = false

			r.Recorder.Event(wp, v1.EventTypeWarning, "ValidationFailed", "Supplied secret is missing one of the required database fields: 'databaseHost', 'database', 'databaseUsername'")

			if err := r.Status().Update(ctx, wp); err != nil {
				logger.Error(err, "Failed to update WordPressSite status with validation failure")
				return ctrl.Result{}, err
			}

			return ctrl.Result{RequeueAfter: time.Second * 120}, nil
		}
	}

	// whenever one of those fields exist, check whether the others also exist

	// Add database reconciliation step - this must happen before deployment
	// This will handle setting up the database resource name in status
	if err := wordpress.ReconcileDatabase(ctx, r.Client, r.Scheme, wp); err != nil {
		logger.Error(err, "Failed to reconcile database")
		return ctrl.Result{}, err
	}

	// Second, reconcile the ConfigMap for the WordPress site
	if err := wordpress.ReconcileConfigMap(ctx, r.Client, r.Scheme, wp); err != nil {
		logger.Error(err, "Failed to reconcile ConfigMap")
		return ctrl.Result{}, err
	}

	// Third, explicitly reconcile the PVC - this must happen before the deployment
	if err := wordpress.ReconcilePVC(ctx, r.Client, r.Scheme, wp); err != nil {
		logger.Error(err, "Failed to reconcile PVC")
		return ctrl.Result{}, err
	}

	// Fourth, reconcile the Deployment
	if err := wordpress.ReconcileDeployment(ctx, r.Client, r.Scheme, wp); err != nil {
		if errors.IsConflict(err) {
			// Conflict detected, retry in a short while
			logger.Info("Conflict detected during Deployment reconciliation, requeing")
			return ctrl.Result{Requeue: true, RequeueAfter: time.Millisecond * 500}, nil
		}
		logger.Error(err, "Failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// Ensure SFTP deployment exists
	if err := wordpress.ReconcileSFTPDeployment(ctx, r.Client, r.Scheme, wp, string(existingSecret.Data["username"])); err != nil {
		logger.Error(err, "Failed to reconcile SFTP Deployment")
		return ctrl.Result{}, err
	}

	// Ensure PHPMyAdmin is reconciled
	if err := wordpress.ReconcilePHPMyAdmin(ctx, r.Client, wp); err != nil {
		logger.Error(err, "Failed to reconcile PhpMyAdmin Deployment")
		return ctrl.Result{}, err
	}

	// Fifth, reconcile the Service
	_, err = wordpress.ReconcileService(ctx, r.Client, r.Scheme, wp)
	if err != nil {
		logger.Error(err, "Failed to reconcile Service")
		return ctrl.Result{}, err
	}

	// Finally, reconcile the Ingress
	if err := wordpress.ReconcileIngress(ctx, r.Client, r.Scheme, wp); err != nil {
		logger.Error(err, "Failed to reconcile Ingress")
		// Don't requeue immediately for Ingress errors to avoid constant loop
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	}

	// Update status
	if err := r.updateStatus(ctx, wp); err != nil {
		logger.Error(err, "Failed to update WordPressSite status")
		return ctrl.Result{}, err
	}

	if wp.Status.DeploymentStatus != StatusWordPressReadyAndDeployed {
		logger.Info("WordPress site is not ready and deployed yet, wait 15 seconds and requeue",
			"name", wp.Name, "namespace", wp.Namespace)
		return ctrl.Result{RequeueAfter: time.Second * 15}, nil
	}

	return ctrl.Result{}, nil
}

// updateStatus updates the status of the WordPressSite resource
func (r *WordPressSiteReconciler) updateStatus(ctx context.Context, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx)

	// Check if WordPress deployment is ready
	status := StatusUnknown

	// Find WordPress pod regardless of CLI setting
	podList := &v1.PodList{}
	labels := wordpress.GetWordpressLabelsForMatching(wp)
	if err := r.List(ctx, podList, client.InNamespace(wp.Namespace), client.MatchingLabels(labels)); err == nil {
		for _, pod := range podList.Items {
			if pod.Status.Phase == v1.PodRunning {
				// Pod is running, check if it's ready
				podReady := false
				for _, condition := range pod.Status.Conditions {
					if condition.Type == v1.ContainersReady && condition.Status == v1.ConditionTrue {
						podReady = true
						r.Recorder.Event(wp, v1.EventTypeNormal, "ContainerReady", "All containers in WordPress pod are ready")
						status = StatusContainerReady
						break
					}
				}

				if podReady {
					break
				}
			}
		}
	} else {
		logger.Error(err, "Failed to list WordPress pods")
	}

	// Check if WordPress is installed & db is ready
	if status == StatusContainerReady {
		// site is not already marked as ready, but a container is running
		installed, err := r.isWordPressInstalled(ctx, wp)
		if err != nil {
			logger.Error(err, "updateStatus: Failed to check if WordPressSite is installed")
		} else if installed {
			// Emit event for installation detected
			r.Recorder.Event(wp, v1.EventTypeNormal, "InstallationDetected", "WordPress installation detected")
			status = StatusWordPressReady
		}
	}

	// Check if Ingress is ready
	if status == StatusWordPressReady {
		ingressReady, err := r.isIngressReady(ctx, wp)
		if err != nil {
			logger.Error(err, "Failed to check if Ingress is ready")
		} else if ingressReady {
			r.Recorder.Event(wp, v1.EventTypeNormal, "IngressReady", "Detected Ingress is ready for WordPress site")
			status = StatusWordPressReadyAndDeployed
		}
	}

	// Try to fetch MySQL version if it's not already set
	if wp.Status.MySQLVersion == "" {
		mysqlVersion, err := r.getMySQLVersionDirect(ctx, wp)
		if err == nil && mysqlVersion != "" {
			wp.Status.MySQLVersion = mysqlVersion
		}
	}

	// Update the WordPress Ready condition based on our determination
	if status == StatusWordPressReadyAndDeployed {
		wordpress.SetCondition(wp, "Ready", metav1.ConditionTrue, "Ready", "WordPress site is ready")
	} else {
		wordpress.SetCondition(wp, "Ready", metav1.ConditionFalse, "NotReady", "WordPress pod not found")
	}

	// Set the Ready status field - this directly updates the status.ready field in the CRD
	wp.Status.DeploymentStatus = status
	wp.Status.Ready = status == StatusWordPressReadyAndDeployed
	wp.Status.LastReconcileTime = &metav1.Time{Time: time.Now()}

	// Update the status
	err := r.Status().Update(ctx, wp)
	if err != nil {
		logger.Error(err, "Failed to update WordPressSite status")
		return err
	}

	return nil
}

// isIngressReady checks if the Ingress resource for the given WordPress site is ready.
func (r *WordPressSiteReconciler) isIngressReady(ctx context.Context, wp *crmv1.WordPressSite) (bool, error) {
	ingress := &networkingv1.Ingress{}
	err := r.Get(ctx, types.NamespacedName{Name: wp.Name, Namespace: wp.Namespace}, ingress)
	if err != nil {
		return false, err
	}
	// Check if LoadBalancer Ingress has at least one address
	if len(ingress.Status.LoadBalancer.Ingress) > 0 {
		addr := ingress.Status.LoadBalancer.Ingress[0]
		return addr.IP != "" || addr.Hostname != "", nil
	}
	return false, nil
}

// getMySQLVersionDirect queries the MySQL version directly
func (r *WordPressSiteReconciler) getMySQLVersionDirect(ctx context.Context, wp *crmv1.WordPressSite) (string, error) {
	// Get database credentials from secret
	dbSecretName := wp.Spec.AdminUserSecretKeyRef
	var dbSecret v1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: dbSecretName, Namespace: wp.Namespace}, &dbSecret)
	if err != nil {
		return "", fmt.Errorf("failed to get database secret: %w", err)
	}

	dbHost := string(dbSecret.Data["databaseHost"])
	if dbHost == "" {
		return "", fmt.Errorf("failed to get database host")
	}
	dbPort := int32(3306) // Default MySQL port
	dbName := string(dbSecret.Data["database"])
	if dbName == "" {
		return "", fmt.Errorf("empty database name")
	}
	dbUser := string(dbSecret.Data["databaseUsername"])
	if dbUser == "" {
		return "", fmt.Errorf("empty database username")
	}
	password := string(dbSecret.Data["password"])
	if password == "" {
		return "", fmt.Errorf("empty database password")
	}

	//logger.V(1).Info("Connecting to MySQL database",
	//	"host", dbHost,
	//	"port", dbPort,
	//	"user", dbUser,
	//	"database", dbName)

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", dbUser, password, dbHost, dbPort, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return "", fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Set a timeout for the query
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Test the connection
	if err := db.PingContext(queryCtx); err != nil {
		return "", fmt.Errorf("failed to ping MySQL server: %w", err)
	}

	// Query MySQL version
	var version string
	err = db.QueryRowContext(queryCtx, "SELECT VERSION()").Scan(&version)
	if err != nil {
		return "", fmt.Errorf("failed to query MySQL version: %w", err)
	}

	return version, nil
}

// Add this function after getMySQLVersionDirect
func (r *WordPressSiteReconciler) isWordPressInstalled(ctx context.Context, wp *crmv1.WordPressSite) (bool, error) {
	// Get database credentials from secret
	dbSecretName := wp.Spec.AdminUserSecretKeyRef
	var dbSecret v1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: dbSecretName, Namespace: wp.Namespace}, &dbSecret)
	if err != nil {
		return false, fmt.Errorf("failed to get database secret: %w", err)
	}

	dbHost := string(dbSecret.Data["databaseHost"])
	if dbHost == "" {
		return false, fmt.Errorf("failed to get database host")
	}
	dbPort := int32(3306) // Default MySQL port
	dbName := string(dbSecret.Data["database"])
	if dbName == "" {
		return false, fmt.Errorf("empty database name")
	}
	dbUser := string(dbSecret.Data["databaseUsername"])
	if dbUser == "" {
		return false, fmt.Errorf("empty database username")
	}
	password := string(dbSecret.Data["password"])
	if password == "" {
		return false, fmt.Errorf("empty database password")
	}

	// Connect to database
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", dbUser, password, dbHost, dbPort, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return false, fmt.Errorf("failed to open MySQL connection: %w", err)
	}
	defer db.Close()

	// Set a timeout for the query
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Test the connection
	if err := db.PingContext(queryCtx); err != nil {
		return false, fmt.Errorf("failed to ping MySQL server: %w", err)
	}

	// Check if wp_options table exists and has site_url option
	var optionValue string
	err = db.QueryRowContext(queryCtx,
		"SELECT option_value FROM wp_options WHERE option_name = 'siteurl' LIMIT 1").Scan(&optionValue)

	if err != nil {
		if err == sql.ErrNoRows {
			// Table exists but no siteurl option
			return false, nil
		}
		// Check if the error indicates the table doesn't exist
		if strings.Contains(err.Error(), "doesn't exist") {
			// wp options table not found
			return false, nil
		}
		return false, fmt.Errorf("failed to query WordPress installation status: %w", err)
	}

	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WordPressSiteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&crmv1.WordPressSite{}).
		Owns(&appsv1.Deployment{}).
		Owns(&v1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&mariadbv1alpha1.Database{}).
		Complete(r)
}

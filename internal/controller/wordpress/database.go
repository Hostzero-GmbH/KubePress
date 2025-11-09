package wordpress

import (
	"context"
	"crypto/rand"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strconv"
	"strings"

	mariadbv1alpha1 "github.com/mariadb-operator/mariadb-operator/v25/api/v1alpha1"
	crmv1 "hostzero.de/m/v2/api/v1"
)

const MariaDBClusterName = "kubepress"

func GenerateRandomString(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)

	// Use crypto/rand for secure random generation
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	// Map random bytes to charset
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}

	return string(b), nil
}

// ReconcileDatabase handles the creation and management of the MySQL database
func ReconcileDatabase(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx).WithValues("component", "database")

	// Create database resource name
	dbResourceName := wp.Name

	if !wp.Spec.Database.CreateNew {
		return nil
	}

	// Check if MariaDBCluster named "kubepress" exists in the namespace
	mariadbCluster := &mariadbv1alpha1.MariaDB{}
	size := resource.MustParse("10Gi")
	err := r.Get(ctx, types.NamespacedName{Name: MariaDBClusterName, Namespace: wp.Namespace}, mariadbCluster)
	if err != nil {
		if errors.IsNotFound(err) {
			// No MariaDBCluster named "kubepress" found, create one

			// create root secret first
			rootSecretName := "kubepress-mariadb-root-secret"

			rootPassword, err := GenerateRandomString(30)
			if err != nil {
				logger.Error(err, "Failed to generate random root password for MariaDB")
				return err
			}

			labels := map[string]string{}
			labels["app.kubernetes.io/managed-by"] = "kubepress-operator"

			rootSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rootSecretName,
					Namespace: wp.Namespace,
					Labels:    labels,
				},
				StringData: map[string]string{
					"password": rootPassword,
				},
			}

			if err := r.Create(ctx, rootSecret); err != nil {
				if !errors.IsAlreadyExists(err) {
					logger.Error(err, "Failed to create root secret for MariaDB")
					return err
				}
			}

			MariaDBReplicas := os.Getenv("MARIADB_REPLICAS")
			replicas, err := strconv.Atoi(MariaDBReplicas)

			if err != nil || replicas < 1 {
				logger.Info("Invalid MARIADB_REPLICAS value, defaulting to 1")
				replicas = 1
			}

			mariadbCluster = &mariadbv1alpha1.MariaDB{
				ObjectMeta: metav1.ObjectMeta{
					Name:      MariaDBClusterName,
					Namespace: wp.Namespace,
					Labels:    labels,
				},
				Spec: mariadbv1alpha1.MariaDBSpec{
					Replicas: int32(replicas),
					Storage: mariadbv1alpha1.Storage{
						Size: &size,
					},
					RootPasswordSecretKeyRef: mariadbv1alpha1.GeneratedSecretKeyRef{
						SecretKeySelector: mariadbv1alpha1.SecretKeySelector{
							LocalObjectReference: mariadbv1alpha1.LocalObjectReference{
								Name: rootSecretName,
							},
							Key: "password",
						},
					},
					Galera: &mariadbv1alpha1.Galera{
						Enabled: replicas > 1,
					},
				},
			}

			if err := r.Create(ctx, mariadbCluster); err != nil {
				logger.Error(err, "Failed to create MariaDBCluster 'kubepress'")
				return err
			}
			logger.Info("Created MariaDBCluster", "name", mariadbCluster.Name)
			// Optionally, return here if you want to wait for cluster readiness before proceeding
		} else {
			logger.Error(err, "Failed to get MariaDBCluster 'kubepress'")
			return err
		}
	}

	// Check if MariaDB Database already exists
	mdb := &mariadbv1alpha1.Database{}
	err = r.Get(ctx, types.NamespacedName{Name: dbResourceName, Namespace: wp.Namespace}, mdb)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get MariaDB Database")
			return err
		}

		// not found, create it
		// Create a new MariaDB Database
		labels := GetDatabaseLabels(wp, map[string]string{
			"app.kubernetes.io/name": "mariadb-database",
		})
		mdb = &mariadbv1alpha1.Database{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dbResourceName,
				Namespace: wp.Namespace,
				Labels:    labels,
			},
			Spec: mariadbv1alpha1.DatabaseSpec{
				MariaDBRef: mariadbv1alpha1.MariaDBRef{
					ObjectReference: mariadbv1alpha1.ObjectReference{
						Name:      MariaDBClusterName, // The name of the cluster you created
						Namespace: wp.Namespace,
					},
				},
				// Optionally set Charset, Collate, etc.
				// Charset: "utf8mb4",
				// Collate: "utf8mb4_unicode_ci",
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(wp, mdb, scheme); err != nil {
			logger.Error(err, "Unable to set owner reference to MariaDB Database", "object", mdb.GetName())
			return err
		}

		if err := r.Create(ctx, mdb); err != nil {
			logger.Error(err, "Failed to create MariaDB Database")
			return err
		}
	}

	// Check if we have a database user in the secret
	secretName := GetDatabaseSecretName(wp.Name)
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: wp.Namespace}, secret)
	if err != nil {
		logger.Error(err, "Failed to get WP secret")
		return err
	}

	// check whether we have a databasePassword key in the secret
	if _, exists := secret.Data["databasePassword"]; !exists {
		// Update the secret with the database password
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		secret.Data["databasePassword"] = secret.Data["password"]

		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "Failed to update WP secret with database password")
			return err
		}
	}

	// checks whether the databaseUsername key exists in the secret
	if _, exists := secret.Data["databaseUsername"]; !exists {
		// no databaseUsername found in secret
		// definitely need to create user, secret, and grant privileges
		// use the username from the admin user secret as name

		// get the WordPress admin username from Spec
		adminUsername := string(secret.Data["username"])
		if adminUsername == "" {
			return fmt.Errorf("username in associated secret is empty")
		}

		// limit combination of those two to 25 characters
		mySqlUniqueUsername := adminUsername

		if len(mySqlUniqueUsername) > 25 {
			// Truncate to 25 characters if too long
			mySqlUniqueUsername = mySqlUniqueUsername[:25]
		}

		// generate a random suffix to ensure uniqueness
		randomSuffix, errRandomSuffix := GenerateRandomString(6)
		if errRandomSuffix != nil {
			logger.Error(errRandomSuffix, "Failed to generate random suffix for MySQL username")
			return errRandomSuffix
		}

		// this username can't be longer than 32 characters in total, because MySQL has a limit of 32 characters for usernames
		mySqlUniqueUsername = fmt.Sprintf("%s-%s", mySqlUniqueUsername, randomSuffix)
		mySqlUniqueUsername = strings.ToLower(mySqlUniqueUsername)

		// check if the username exists already
		existingUser := &mariadbv1alpha1.User{}
		err = r.Get(ctx, types.NamespacedName{Name: mySqlUniqueUsername, Namespace: wp.Namespace}, existingUser)
		if err == nil {
			// user already exists, this is very unlikely because of the random suffix, but just in case
			logger.Info("Generated MySQL username already exists, generating a new one", "username", mySqlUniqueUsername)
			return fmt.Errorf("generated MySQL username already exists, please retry")
		}

		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to check if MySQL user exists")
			return err
		}

		// user not found, create it
		err = CreateDatabaseUser(ctx, r, scheme, wp, dbResourceName, mySqlUniqueUsername, secretName)

		if err != nil {
			logger.Error(err, "Failed to create database user")
			return err
		}

		// Update the secret with the database username
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		secret.Data["databaseUsername"] = []byte(mySqlUniqueUsername)
		secret.Data["database"] = []byte(dbResourceName)
		secret.Data["databaseHost"] = []byte(fmt.Sprintf("%s.%s.svc.cluster.local", MariaDBClusterName, wp.Namespace))

		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "Failed to update WP secret with database username")
			return err
		}
	} else {
		// existing databaseUsername found in secret
		databaseUsername := string(secret.Data["databaseUsername"])

		// databaseUsername exists, check if also the user exists in MariaDB, if not, create it
		// check if the username exists already
		existingUser := &mariadbv1alpha1.User{}
		err = r.Get(ctx, types.NamespacedName{Name: databaseUsername, Namespace: wp.Namespace}, existingUser)

		if err == nil {
			// user already exists
			// we don't need to do anything here
			return nil
		}

		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to check if MySQL user exists")
			return err
		}

		// user not found, create it
		err = CreateDatabaseUser(ctx, r, scheme, wp, dbResourceName, databaseUsername, secretName)

		if err != nil {
			logger.Error(err, "Failed to create database user")
			return err
		}

		// Update the secret with the database username
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		secret.Data["databaseUsername"] = []byte(databaseUsername)
		secret.Data["database"] = []byte(dbResourceName)
		secret.Data["databaseHost"] = []byte(fmt.Sprintf("%s.%s.svc.cluster.local", MariaDBClusterName, wp.Namespace))

		if err := r.Update(ctx, secret); err != nil {
			logger.Error(err, "Failed to update WP secret with database username")
			return err
		}
	}

	return nil
}

// create user function
func CreateDatabaseUser(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite, dbResourceName string, username string, secretName string) error {
	logger := log.FromContext(ctx).WithValues("component", "database-user")

	labels := GetDatabaseLabels(wp, map[string]string{
		"app.kubernetes.io/name": "mariadb-user",
	})

	user := &mariadbv1alpha1.User{}
	user.ObjectMeta = metav1.ObjectMeta{
		Name:      username,
		Namespace: wp.Namespace,
		Labels:    labels,
	}
	user.Spec = mariadbv1alpha1.UserSpec{
		MariaDBRef: mariadbv1alpha1.MariaDBRef{
			ObjectReference: mariadbv1alpha1.ObjectReference{
				Name:      MariaDBClusterName,
				Namespace: wp.Namespace,
			},
		},
		Host: "%", // Allow access from any host, adjust as needed
		PasswordSecretKeyRef: &mariadbv1alpha1.SecretKeySelector{
			LocalObjectReference: mariadbv1alpha1.LocalObjectReference{
				Name: secretName,
			},
			Key: "databasePassword",
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(wp, user, scheme); err != nil {
		logger.Error(err, "Unable to set owner reference to MariaDB User", "object", user.GetName())
		return err
	}

	if err := r.Create(ctx, user); err != nil {
		logger.Error(err, "Failed to create MySQL user")
		return err
	}

	// grant privileges to the user on the database
	labels = GetDatabaseLabels(wp, map[string]string{
		"app.kubernetes.io/name": "mariadb-grant",
	})
	grant := &mariadbv1alpha1.Grant{}
	HostString := "%"
	grant.ObjectMeta = metav1.ObjectMeta{
		Name:      fmt.Sprintf("grant-%s", username),
		Namespace: wp.Namespace,
		Labels:    labels,
	}
	grant.Spec = mariadbv1alpha1.GrantSpec{
		MariaDBRef: mariadbv1alpha1.MariaDBRef{
			ObjectReference: mariadbv1alpha1.ObjectReference{
				Name:      MariaDBClusterName,
				Namespace: wp.Namespace,
			},
		},
		Database: dbResourceName,
		Username: username,
		Privileges: []string{
			"ALL PRIVILEGES",
		},
		Table: "*",
		Host:  &HostString,
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(wp, grant, scheme); err != nil {
		logger.Error(err, "Unable to set owner reference", "object", grant.GetName())
		return err
	}

	if err := r.Create(ctx, grant); err != nil {
		logger.Error(err, "Failed to create MySQL grant")
		return err
	}

	return nil
}

# User Guide

Welcome to the User Guide! This document provides comprehensive instructions to help you effectively use KubePress.

As explained in the Quick Start, deploying a WordPress instance with KubePress involves two steps:
1. Create a Secret containing the WordPress credentials (These will be used as the intial username/password for WordPress, SFTP and the Database user).
2. Create a WordPress Custom Resource that references the Secret created in step 1.

### Create a Secret for WordPress Credentials

To create a WordPress instance, you need to create a secret first.

A fully functional Secret for a WordPress Instance looks like this:

```yaml
apiVersion: v1  
data:
  database: d3AtLWhvc3R6ZXJvLS13MS1jb20=
  databaseHost: a3ViZXByZXNzLmt1YmVwcmVzcy5zdmMuY2x1c3Rlci5sb2NhbA==
  databaseUsername: aG9zdHplcm8tYWRtaW4tYWIyOXNr
  databasePassword: cGFzc3dvcmQK
  password: cGFzc3dvcmQK
  username: YWRtaW4=
kind: Secret
metadata:
  name: wp--hostzero--w1-com
  namespace: kubepress
type: Opaque
```

The secret must contain the following keys. These are required in any case:
- `username`
- `password`

Optionally, you can also provide:
- `databaseUsername`
    - If this field is provided in the secret, it will be used to create the database user. If not provided, the value from `username` will be used with a random suffix appended to it. The random suffix is to ensure uniqueness of the database user, because all database users will be created in the same database cluster.
- `databasePassword`
    - If this field is provided in the secret, it will be used to create the database user. If not provided, a random password will be generated for the database user. This is optionally independent of the `databaseUsername` field.

If you set the `wp.Spec.Database.CreateNew` to `false`, you must provide the following additional keys in the secret (in addition to `username` and `password`):
- `database`
    - The name of the existing database to use.
- `databaseHost`
    - The host of the existing database to use (and connect to).
- `databaseUsername`
    - The username of the existing database user to use.
- `databasePassword`
    - The password of the existing database user to use.

If you set the `wp.Spec.Database.CreateNew` to `true` (which is the default), you do not need to provide the three additional keys from above. The operator will create a new database and database user for you.

This data will be used to create:
- the SFTP user
- the Database user
- the initial WordPress admin user
- (and also indirectly the access to PHPMyAdmin, because you will need the Database user to access PHPMyAdmin)

After the initial creation of the WordPress instance, an update of username/password in the secret will not update:
- the Admin user in WordPress
- the password in the Database
    - However, you can set a label to the secret, and the MariaDB operator will pick up the change and update the password in the database. According to the [MariaDB Operator documentation](https://github.com/mariadb-operator/mariadb-operator/blob/main/docs/api_reference.md#userspec), you can set the label `k8s.mariadb.com/watch` on the secret, and the operator will watch for changes.

The newly updated credentials will be used in the deployment after a restart of the pods.

### Create a WordPress Custom Resource

After creating the secret, you can create a WordPress Custom Resource that references the secret.

A fully functional WordPress Custom Resource looks like this:

```yaml
apiVersion: crm.hostzero.de/v1
kind: WordPressSite
metadata:
  name: wp--w2-com
  namespace: kubepress
spec:
  adminEmail: admin@example.com
  adminUserSecretKeyRef: wp--w2-com
  database:
    createNew: true
  ingress:
    host: w2.com
    tls: false
  siteTitle: w2
  wordpress:
    image: wordpress:latest
    maxUploadLimit: 64M
    replicas: 1
    resources:
      cpuLimit: 500m
      cpuRequest: 250m
      memoryLimit: 1Gi
      memoryRequest: 512Mi
    storageSize: 10Gi
```

For more information about the fields in the WordPress Custom Resource, please look directly at the [wordpresssite_types.go](../api/v1/wordpresssite_types.go) file in the `api/v1` directory.
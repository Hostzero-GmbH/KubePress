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

### Admin SSO (one-click login)

Admin SSO is disabled by default. Enabling it lets a control plane that has access to the
site's Secret log users into wp-admin without a password:

```yaml
spec:
  wordpress:
    adminSSO:
      enabled: true
```

The operator then generates a random key, stores it as `ssoKey` in the site's Secret (the one
referenced by `adminUserSecretKeyRef`) and installs a must-use plugin
(`wp-content/mu-plugins/kubepress-sso.php`) that is re-written on every pod start. To log a
user in, redirect the browser to

```
https://<host>/?kubepress_sso=<base64url(payload)>.<base64url(HMAC-SHA256(payload, key))>
```

where `payload` is a JSON object with `email` (the WordPress user to log in), `exp` (unix
timestamp, at most 5 minutes ahead) and `nonce` (random, accepted once). Optional fields:
`login` and `name` are used when the user has to be created, `create: false` prevents
creating a missing user (new users become administrators), `redirect` is a wp-admin
relative path to land on. Tokens are only accepted over HTTPS. Rotate the key by removing
`ssoKey` from the Secret; the operator generates a new one. Setting `enabled: false`
removes the plugin on the next pod start.

For more information about the fields in the WordPress Custom Resource, please look directly at the [wordpresssite_types.go](../api/v1/wordpresssite_types.go) file in the `api/v1` directory.
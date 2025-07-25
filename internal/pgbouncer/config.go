// Copyright 2021 - 2025 Crunchy Data Solutions, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package pgbouncer

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/crunchydata/postgres-operator/internal/collector"
	"github.com/crunchydata/postgres-operator/internal/naming"
	"github.com/crunchydata/postgres-operator/pkg/apis/postgres-operator.crunchydata.com/v1beta1"
)

const (
	configDirectory = "/etc/pgbouncer"

	authFileAbsolutePath  = configDirectory + "/" + authFileProjectionPath
	emptyFileAbsolutePath = configDirectory + "/" + emptyFileProjectionPath
	iniFileAbsolutePath   = configDirectory + "/" + iniFileProjectionPath

	authFileProjectionPath  = "~postgres-operator/users.txt"
	emptyFileProjectionPath = "pgbouncer.ini"
	iniFileProjectionPath   = "~postgres-operator.ini"

	authFileSecretKey   = "pgbouncer-users.txt" // #nosec G101 this is a name, not a credential
	passwordSecretKey   = "pgbouncer-password"  // #nosec G101 this is a name, not a credential
	verifierSecretKey   = "pgbouncer-verifier"  // #nosec G101 this is a name, not a credential
	emptyConfigMapKey   = "pgbouncer-empty"
	iniFileConfigMapKey = "pgbouncer.ini"
)

const (
	iniGeneratedWarning = "" +
		"# Generated by postgres-operator. DO NOT EDIT.\n" +
		"# Your changes will not be saved.\n"
)

type iniValueSet map[string]string

func (vs iniValueSet) String() string {
	keys := make([]string, 0, len(vs))
	for k := range vs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		if len(vs[k]) <= 0 {
			_, _ = fmt.Fprintf(&b, "%s =\n", k)
		} else {
			_, _ = fmt.Fprintf(&b, "%s = %s\n", k, vs[k])
		}
	}
	return b.String()
}

// authFileContents returns a PgBouncer user database.
func authFileContents(password string) []byte {
	// > There should be at least 2 fields, surrounded by double quotes.
	// > Double quotes in a field value can be escaped by writing two double quotes.
	// - https://www.pgbouncer.org/config.html#authentication-file-format
	quote := func(s string) string {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}

	user1 := quote(PostgresqlUser) + " " + quote(password) + "\n"

	return []byte(user1)
}

func clusterINI(ctx context.Context, cluster *v1beta1.PostgresCluster) string {
	var (
		pgBouncerPort = *cluster.Spec.Proxy.PGBouncer.Port
		postgresPort  = *cluster.Spec.Port
	)

	global := iniValueSet{
		// Prior to PostgreSQL v12, the default setting for "extra_float_digits"
		// does not return precise float values. Applications that want
		// consistent results from different PostgreSQL versions may connect
		// with this startup parameter. The JDBC driver uses it regardless.
		// Trust that applications that know or care about this setting are
		// using it consistently within each connection pool.
		// - https://www.postgresql.org/docs/current/runtime-config-client.html#GUC-EXTRA-FLOAT-DIGITS
		// - https://github.com/pgjdbc/pgjdbc/blob/REL42.2.19/pgjdbc/src/main/java/org/postgresql/core/v3/ConnectionFactoryImpl.java#L334
		"ignore_startup_parameters": "extra_float_digits",

		// Authenticate frontend connections using passwords stored in PostgreSQL.
		// PgBouncer will connect to the backend database that is requested by
		// the frontend as the "auth_user" and execute "auth_query". When
		// "auth_user" requires a password, PgBouncer reads it from "auth_file".
		"auth_file":  authFileAbsolutePath,
		"auth_query": "SELECT username, password from pgbouncer.get_auth($1)",
		"auth_user":  PostgresqlUser,

		// TODO(cbandy): Use an HBA file to control authentication of PgBouncer
		// accounts; e.g. "admin_users" below.
		// - https://www.pgbouncer.org/config.html#hba-file-format
		//"auth_hba_file": "",
		//"auth_type":     "hba",
		//"admin_users": "pgbouncer",

		// Require TLS encryption on client connections.
		"client_tls_sslmode":   "require",
		"client_tls_cert_file": certFrontendAbsolutePath,
		"client_tls_key_file":  certFrontendPrivateKeyAbsolutePath,
		"client_tls_ca_file":   certFrontendAuthorityAbsolutePath,

		// Listen on the PgBouncer port on all addresses.
		"listen_addr": "*",
		"listen_port": fmt.Sprint(pgBouncerPort),

		// Require TLS encryption on connections to PostgreSQL.
		"server_tls_sslmode": "verify-full",
		"server_tls_ca_file": certBackendAuthorityAbsolutePath,

		// Disable Unix sockets to keep the filesystem read-only.
		"unix_socket_dir": "",
	}

	// If OpenTelemetryLogs feature is enabled, enable logging to file
	if collector.OpenTelemetryLogsEnabled(ctx, cluster) {
		global["logfile"] = naming.PGBouncerLogPath + "/pgbouncer.log"
	}

	// When OTel metrics are enabled, allow pgBouncer's postgres user
	// to run read-only console queries on pgBouncer's virtual db
	if collector.OpenTelemetryMetricsEnabled(ctx, cluster) {
		global["stats_users"] = PostgresqlUser
	}

	// Override the above with any specified settings.
	maps.Copy(global, cluster.Spec.Proxy.PGBouncer.Config.Global)

	// Prevent the user from bypassing the main configuration file.
	global["conffile"] = iniFileAbsolutePath

	// Use a wildcard to automatically create connection pools based on database
	// names. These pools connect to cluster's primary service. The service name
	// is an RFC 1123 DNS label so it does not need to be quoted nor escaped.
	// - https://www.pgbouncer.org/config.html#section-databases
	//
	// NOTE(cbandy): PgBouncer only accepts connections to items in this section
	// and the database "pgbouncer", which is the admin console. For connections
	// to the wildcard, PgBouncer first checks for the database in PostgreSQL.
	// When that database does not exist, the client will experience timeouts
	// or errors that sound like PgBouncer misconfiguration.
	// - https://github.com/pgbouncer/pgbouncer/issues/352
	databases := iniValueSet{
		"*": fmt.Sprintf("host=%s port=%d",
			naming.ClusterPrimaryService(cluster).Name, postgresPort),
	}

	// Replace the above with any specified databases.
	if len(cluster.Spec.Proxy.PGBouncer.Config.Databases) > 0 {
		databases = iniValueSet(cluster.Spec.Proxy.PGBouncer.Config.Databases)
	}

	users := iniValueSet(cluster.Spec.Proxy.PGBouncer.Config.Users)

	// Include any custom configuration file, then apply global settings, then
	// pool definitions.
	result := iniGeneratedWarning +
		"\n[pgbouncer]" +
		"\n%include " + emptyFileAbsolutePath +
		"\n\n[pgbouncer]\n" + global.String() +
		"\n[databases]\n" + databases.String()

	if len(users) > 0 {
		result += "\n[users]\n" + users.String()
	}

	return result
}

// podConfigFiles returns projections of PgBouncer's configuration files to
// include in the configuration volume.
func podConfigFiles(
	config v1beta1.PGBouncerConfiguration,
	configmap *corev1.ConfigMap, secret *corev1.Secret,
) []corev1.VolumeProjection {
	// Start with an empty file at /etc/pgbouncer/pgbouncer.ini. This file can
	// be overridden by the user, but it must exist because our configuration
	// file refers to it.
	projections := []corev1.VolumeProjection{
		{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configmap.Name,
				},
				Items: []corev1.KeyToPath{{
					Key:  emptyConfigMapKey,
					Path: emptyFileProjectionPath,
				}},
			},
		},
	}

	// Add any specified projections. These may override the files above.
	// - https://docs.k8s.io/concepts/storage/volumes/#projected
	projections = append(projections, config.Files...)

	// Add our non-empty configurations last so that they take precedence.
	projections = append(projections, []corev1.VolumeProjection{
		{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: configmap.Name,
				},
				Items: []corev1.KeyToPath{{
					Key:  iniFileConfigMapKey,
					Path: iniFileProjectionPath,
				}},
			},
		},
		{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secret.Name,
				},
				Items: []corev1.KeyToPath{{
					Key:  authFileSecretKey,
					Path: authFileProjectionPath,
				}},
			},
		},
	}...)

	return projections
}

// reloadCommand returns an entrypoint that convinces PgBouncer to reload
// configuration files. The process will appear as name in `ps` and `top`.
func reloadCommand(name string) []string {
	// Use a Bash loop to periodically check the mtime of the mounted
	// configuration volume. When it changes, signal PgBouncer and print the
	// observed timestamp.
	//
	// Coreutils `sleep` uses a lot of memory, so the following opens a file
	// descriptor and uses the timeout of the builtin `read` to wait. That same
	// descriptor gets closed and reopened to use the builtin `[ -nt` to check
	// mtimes.
	// - https://unix.stackexchange.com/a/407383
	const script = `
exec {fd}<> <(:||:)
while read -r -t 5 -u "${fd}" ||:; do
  if [[ "${directory}" -nt "/proc/self/fd/${fd}" ]] && pkill -HUP --exact pgbouncer
  then
    exec {fd}>&- && exec {fd}<> <(:||:)
    stat --format='Loaded configuration dated %y' "${directory}"
  fi
done
`

	// Elide the above script from `ps` and `top` by wrapping it in a function
	// and calling that.
	wrapper := `monitor() {` + script + `}; export directory="$1"; export -f monitor; exec -a "$0" bash -ceu monitor`

	return []string{"bash", "-ceu", "--", wrapper, name, configDirectory}
}

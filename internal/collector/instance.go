// Copyright 2024 - 2025 Crunchy Data Solutions, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"

	corev1 "k8s.io/api/core/v1"

	"github.com/crunchydata/postgres-operator/internal/config"
	"github.com/crunchydata/postgres-operator/internal/feature"
	"github.com/crunchydata/postgres-operator/internal/initialize"
	"github.com/crunchydata/postgres-operator/internal/naming"
	"github.com/crunchydata/postgres-operator/pkg/apis/postgres-operator.crunchydata.com/v1beta1"
)

// AddToConfigMap populates the shared ConfigMap with fields needed to run the Collector.
func AddToConfigMap(
	ctx context.Context,
	inConfig *Config,
	outInstanceConfigMap *corev1.ConfigMap,
) error {
	var err error
	if outInstanceConfigMap.Data == nil {
		outInstanceConfigMap.Data = make(map[string]string)
	}

	outInstanceConfigMap.Data["collector.yaml"], err = inConfig.ToYAML()

	return err
}

// AddToPod adds the OpenTelemetry collector container to a given Pod
func AddToPod(
	ctx context.Context,
	spec *v1beta1.InstrumentationSpec,
	pullPolicy corev1.PullPolicy,
	inInstanceConfigMap *corev1.ConfigMap,
	outPod *corev1.PodSpec,
	volumeMounts []corev1.VolumeMount,
	sqlQueryPassword string,
) {
	if !(feature.Enabled(ctx, feature.OpenTelemetryLogs) || feature.Enabled(ctx, feature.OpenTelemetryMetrics)) {
		return
	}

	configVolumeMount := corev1.VolumeMount{
		Name:      "collector-config",
		MountPath: "/etc/otel-collector",
		ReadOnly:  true,
	}
	configVolume := corev1.Volume{Name: configVolumeMount.Name}
	configVolume.Projected = &corev1.ProjectedVolumeSource{
		Sources: []corev1.VolumeProjection{{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: inInstanceConfigMap.Name,
				},
				Items: []corev1.KeyToPath{{
					Key:  "collector.yaml",
					Path: "config.yaml",
				}},
			},
		}},
	}

	// If the user has specified files to be mounted in the spec, add them to the projected config volume
	if spec != nil && spec.Config != nil && spec.Config.Files != nil {
		configVolume.Projected.Sources = append(configVolume.Projected.Sources, spec.Config.Files...)
	}

	container := corev1.Container{
		Name:            naming.ContainerCollector,
		Image:           config.CollectorContainerImage(spec),
		ImagePullPolicy: pullPolicy,
		Command:         []string{"/otelcol-contrib", "--config", "/etc/otel-collector/config.yaml"},
		Env: []corev1.EnvVar{
			{
				Name: "K8S_POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				}},
			},
			{
				Name: "K8S_POD_NAME",
				ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				}},
			},
			{
				Name:  "PGPASSWORD",
				Value: sqlQueryPassword,
			},
		},

		SecurityContext: initialize.RestrictedSecurityContext(),
		VolumeMounts:    append(volumeMounts, configVolumeMount),
	}

	if feature.Enabled(ctx, feature.OpenTelemetryMetrics) {
		container.Ports = []corev1.ContainerPort{{
			ContainerPort: int32(8889),
			Name:          "otel-metrics",
			Protocol:      corev1.ProtocolTCP,
		}}
	}

	outPod.Containers = append(outPod.Containers, container)
	outPod.Volumes = append(outPod.Volumes, configVolume)
}

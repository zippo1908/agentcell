package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/zippo1908/agentcell/internal/useruid"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// brokerTokenVolume is an audience-bound projected ServiceAccount token
// (ADR-0005 hardening): scoped to the broker so it cannot be replayed
// against the apiserver, and 1h-lived.
func brokerTokenVolume() corev1.Volume {
	return corev1.Volume{
		Name: runtimeapi.BrokerTokenVolume,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{
					ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Audience:          runtimeapi.BrokerAudience,
						ExpirationSeconds: ptr.To[int64](3600),
						Path:              "token",
					},
				}},
			},
		},
	}
}

func brokerTokenMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      runtimeapi.BrokerTokenVolume,
		MountPath: runtimeapi.BrokerTokenMount,
		ReadOnly:  true,
	}
}

// withBrokerClientLabel marks a pod as permitted to reach the broker; the
// NetworkPolicy egress rule selects on it.
func withBrokerClientLabel(labels map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		out[k] = v
	}
	out[runtimeapi.BrokerClientLabelKey] = runtimeapi.BrokerClientLabelVal
	return out
}

// Every pod the platform renders — anchor, session, settle, prod — runs
// non-root with the same hardened defaults.
//
// podSecurity is the project identity: the anchor and the production pod
// serve the shared checkout and belong to nobody in particular.
func podSecurity() *corev1.PodSecurityContext {
	return podSecurityAs(useruid.ProjectUID)
}

// podSecurityAs runs a pod as one specific user (ADR-0009).
//
// The UID is the filesystem expression of the boundary, not the boundary
// itself — that is the pod. Two users' sessions are separate pods with
// separate UIDs, so one cannot read the other's private tree even though
// both mount the same project volume.
//
// fsGroup stays the project group for exactly one reason: it is what lets
// privately-owned processes still collaborate on the shared checkout. Group
// membership grants the project layer, the UID withholds everything else.
func podSecurityAs(uid int64) *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(uid),
		RunAsGroup:     ptr.To(useruid.ProjectGID),
		FSGroup:        ptr.To(useruid.ProjectGID),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func containerSecurity() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// gitCredEnv maps the kubernetes.io/basic-auth secret keys (username /
// password) onto the exact variable names the askpass shim reads. Explicit
// mapping — never envFrom: envFrom would inject the raw key names, which
// askpass ignores and env filters don't know about.
func gitCredEnv(secretName string) []corev1.EnvVar {
	if secretName == "" {
		return nil
	}
	ref := func(key string) *corev1.EnvVarSource {
		return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		}}
	}
	return []corev1.EnvVar{
		{Name: "GIT_USERNAME", ValueFrom: ref("username")},
		{Name: "GIT_TOKEN", ValueFrom: ref("password")},
	}
}

package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// Every pod the platform renders — anchor, session, settle, prod — runs
// non-root with the same hardened defaults. The devbox image ships a
// uid-1000 user; fsGroup makes the shared PVC and emptyDirs writable.
func podSecurity() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To[int64](1000),
		RunAsGroup:     ptr.To[int64](1000),
		FSGroup:        ptr.To[int64](1000),
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

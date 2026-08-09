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

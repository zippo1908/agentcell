package controller

import (
	corev1 "k8s.io/api/core/v1"
)

// egressEnv points a workload's outbound HTTP at the egress proxy.
//
// Set through the conventional variables rather than anything of our own,
// because every tool an agent runs already honours them: curl, git, Go,
// Node, Python, and the model CLIs themselves. A bespoke mechanism would
// have to be taught to each of them one at a time, and the ones we forgot
// would quietly keep their unrestricted route out.
//
// NO_PROXY is the part that is easy to get wrong and expensive to get wrong.
// Everything inside the cluster must bypass the proxy: the git broker, the
// Kubernetes API, other services, the pod network itself. Sending those
// through an egress allowlist would refuse them — the allowlist names
// external destinations — and a runtime that cannot reach the broker cannot
// push, which looks like a git failure and is really a proxy misconfiguration.
//
// Both cases of each name are set. The lowercase spelling is what most
// libraries actually read; the uppercase is what people expect to see. Tools
// disagree about which wins, so setting one and not the other produces a
// system where some traffic is proxied and some is not, which is the worst
// of both.
func egressEnv(proxyURL string) []corev1.EnvVar {
	if proxyURL == "" {
		return nil
	}
	const noProxy = "localhost,127.0.0.1,::1," +
		".svc,.svc.cluster.local,.cluster.local," +
		"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16"
	return []corev1.EnvVar{
		{Name: "HTTP_PROXY", Value: proxyURL},
		{Name: "http_proxy", Value: proxyURL},
		{Name: "HTTPS_PROXY", Value: proxyURL},
		{Name: "https_proxy", Value: proxyURL},
		{Name: "NO_PROXY", Value: noProxy},
		{Name: "no_proxy", Value: noProxy},
	}
}

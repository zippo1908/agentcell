package webui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

// ExecIn adapts execIn to the shape the Session controller injects, so both
// halves of the control plane reach into pods the same way.
func ExecIn(cfg *rest.Config, kube kubernetes.Interface) func(context.Context, string, string, []string, io.Reader) (string, error) {
	return func(ctx context.Context, ns, pod string, argv []string, stdin io.Reader) (string, error) {
		return execIn(ctx, cfg, kube, ns, pod, argv, stdin)
	}
}

// execIn runs a command inside a pod and returns its combined output.
//
// Shared by the console (status, follow-ups) and the Session controller
// (opening and closing windows in a user's runtime). Both need it for the
// same reason: the pods on the other end deliberately hold no API credential
// of their own (ADR-0005), so the control plane reaches in rather than the
// workload reaching out.
func execIn(ctx context.Context, cfg *rest.Config, kube kubernetes.Interface,
	ns, pod string, argv []string, stdin io.Reader) (string, error) {
	container, err := firstContainer(ctx, kube, ns, pod)
	if err != nil {
		return "", err
	}
	req := kube.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	exe, err := remotecommand.NewSPDYExecutor(cfg, http.MethodPost, req.URL())
	if err != nil {
		return "", err
	}
	var out, errOut bytes.Buffer
	err = exe.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: stdin, Stdout: &out, Stderr: &errOut,
	})
	return out.String() + errOut.String(), err
}

// firstContainer avoids hard-coding a name: a session pod's container is
// "session", a user runtime's is "runtime", and the caller has no business
// knowing which shape a session happens to have.
func firstContainer(ctx context.Context, kube kubernetes.Interface, ns, pod string) (string, error) {
	p, err := kube.CoreV1().Pods(ns).Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	if len(p.Spec.Containers) == 0 {
		return "", fmt.Errorf("pod %s/%s has no containers", ns, pod)
	}
	return p.Spec.Containers[0].Name, nil
}

var _ = types.NamespacedName{}

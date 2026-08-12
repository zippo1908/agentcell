package controller

import (
	corev1 "k8s.io/api/core/v1"

	acv1 "github.com/zippo1908/agentcell/api/v1alpha1"
	"github.com/zippo1908/agentcell/pkg/runtimeapi"
)

// gitWorkloadEnv returns the git-related env for a workload container that
// performs git operations (anchor, prod-clone, settle). In broker mode
// (ADR-0005) it injects only the broker URL and cell name — never a forge
// credential; the pod authenticates to the broker with its ServiceAccount
// token. In direct mode it injects the mapped forge credentials.
func gitWorkloadEnv(brokerURL, cellName, secretName string) []corev1.EnvVar {
	if brokerURL != "" {
		return []corev1.EnvVar{
			{Name: runtimeapi.EnvGitBroker, Value: brokerURL},
			{Name: runtimeapi.EnvCellName, Value: cellName},
		}
	}
	return gitCredEnv(secretName)
}

// brokerMode reports whether git routes through the broker for this cell.
func brokerMode(brokerURL string, cell *acv1.Cell) bool {
	return brokerURL != "" && cell.Spec.Repo.SecretName != ""
}

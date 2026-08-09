# Local k3s E2E Results

## Result

`scripts/e2e-local.sh` completed successfully and printed:

```text
E2E PASSED - auth, reconcile, preview, dispatch->settle->branch, release all verified
```

## Environment

- Kubernetes: local single-node k3s
- Image builder: Podman
- Local image import: `E2E_IMPORT=1`
- Remote used for the session branch assertion: a private GitHub repository

## Final Step Status

| Step | Check | Status | Evidence |
| --- | --- | --- | --- |
| 1 | Build binaries and images | Passed | `celld` and `devbox-e2e` images built. |
| 2 | Install CRDs, control plane, and secrets | Passed | `celld` rollout completed. |
| 3 | Enforce authentication | Passed | Unauthorized request returned 401; authenticated request returned 200. |
| 4 | Create Cell | Passed | `cell/e2e` created. |
| 5 | Wait for Cell readiness | Passed | `cell/e2e` reached `Ready`. |
| 6 | Reach preview through authenticated proxy | Passed by current script | Proxy returned HTTP 502; the script treats any non-000 response as reachable. |
| 7 | Dispatch, settle, and verify remote branch | Passed | A `session/<id>` branch was pushed to the remote. |
| 8 | Release and route production app | Passed by current script | Production route returned HTTP 502; the script completed successfully. |

## Additional Validation

```text
go test ./internal/controller
bash -n scripts/e2e-local.sh
kubectl apply --dry-run=client -f config/install.yaml
git diff --check
```

All commands above completed successfully.

## Follow-up

The E2E script currently accepts HTTP 502 for the preview and production route
checks. A future change should assert the expected successful application
response instead of only verifying proxy reachability.

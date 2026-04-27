## Failure Scenario Handling

### a. API crashes during peak hours
Our API runs with a minimum of 2 replicas spread across nodes via `podAntiAffinity`.
If one pod crashes, Kubernetes automatically restarts it while the other replica continues
serving traffic — the Service stops routing to the crashed pod once it fails the readiness probe.

**Detection:** `PodCrashLooping` alert and `HighErrorRate` alert fire in Prometheus.

**Manual response:**
\```bash
kubectl logs deployment/api-deployment -n devops-prod --previous
kubectl rollout undo deployment/api-deployment -n devops-prod
\```

**Prevention:** Review resource limits, add load testing to CI pipeline.

---

### b. Worker fails and infinitely retries
Kubernetes restarts the worker with exponential backoff (not immediate infinite loop),
so it will not hammer the system. The backoff resets once the pod runs successfully for 10 minutes.

**Detection:** `WorkerStalledJob` alert fires when `worker_jobs_total` stops incrementing for 10 min.
Pod shows `CrashLoopBackOff` in `kubectl get pods`.

**Manual response:**
\```bash
kubectl logs deployment/worker-deployment -n devops-prod --previous
kubectl rollout restart deployment/worker-deployment -n devops-prod
\```

**Prevention:** Add error handling inside worker to skip bad records instead of crashing.
Add a `backoffLimit` if migrating worker to a Kubernetes Job.

---

### c. Bad deployment is released
Kubernetes rolling update strategy ensures old pods stay up until new pods pass
the readiness probe. If new pods never become Ready, the rollout stalls and
old pods continue serving traffic automatically.

**Detection:** `HighErrorRate` alert fires shortly after deploy. Readiness probe failures
visible in `kubectl rollout status`.

**Manual response:**
\```bash
kubectl rollout undo deployment/api-deployment -n devops-prod
\```

**Prevention:** Add smoke tests in the deploy stage that auto-rollback on failure.
Long-term: implement canary deployment so only a small traffic percentage hits new version first.

---

### d. Kubernetes node goes down
Pods spread across nodes via `podAntiAffinity` means a single node failure
does not take down all replicas. Kubernetes reschedules affected pods to
healthy nodes automatically (after ~5 min node controller timeout).

**Detection:** `kube_node_status_condition` alert in Prometheus. `HighErrorRate`
may also fire briefly during rescheduling.

**Manual response:**
\```bash
kubectl get nodes                            # identify failed node
kubectl describe node <node-name>            # inspect events
kubectl drain <node-name> --ignore-daemonsets  # if planned maintenance
\```

**Prevention:** Run minimum 3 nodes across availability zones.
Add `PodDisruptionBudget` to guarantee at least 1 API replica always available.

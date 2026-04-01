# Scaling Design

## Problem Statement

Pulse must support thousands of canary CRs without becoming a bottleneck. Naive controller patterns break at scale in specific, predictable ways.

## Failure Modes at Scale

### 1. Reconcile Storm (N reconciles per batch)

**Problem:** Using `For(&HttpCanary{})` creates one work queue entry per CR. If 1,000 CRs are created simultaneously, the controller runs 1,000 identical reconciles — each listing all CRs, rebuilding the ConfigMap, and ensuring the Deployment.

**Solution:** `Watches()` with `EnqueueRequestsFromMapFunc` maps all events to a single fixed key. The work queue deduplicates same-key entries, so 1,000 events = 1 reconcile.

```go
Watches(&canaryv1alpha1.HttpCanary{},
    handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []ctrl.Request {
        return []ctrl.Request{triggerKey}
    }),
)
```

### 2. Status Sync Amplification (N^2 writes per interval)

**Problem:** Putting status polling inside `Reconcile()` with `RequeueAfter(15s)` means each of N CRs independently requeues. Each reconcile polls `/results` and updates N statuses.

| CRs | Reconciles/15s | Status writes/15s |
|-----|---------------|-------------------|
| 10 | 10 | 100 |
| 100 | 100 | 10,000 |
| 1,000 | 1,000 | 1,000,000 |

**Solution:** A separate `StatusSyncer` Runnable runs on a fixed timer. One poll, one CR list, N status writes — regardless of N.

| CRs | Polls/15s | Status writes/15s (worst case) |
|-----|----------|-------------------------------|
| 10 | 1 | 10 |
| 100 | 1 | 100 |
| 1,000 | 1 | 1,000 |

### 3. Unnecessary Status Writes (writes without changes)

**Problem:** Writing `.status` on every sync cycle even when nothing changed generates unnecessary API server load and triggers spurious watch events (which can cascade into more reconciles).

**Solution:** `statusChanged()` compares the probe result against the CR's current status. In steady state (most probes remain Healthy), almost no writes occur.

## Current Scaling Profile

| Dimension | Approach | Complexity |
|-----------|----------|------------|
| CR event handling | Single-key dedup | O(1) reconciles per batch |
| Config rebuild | Full list + rebuild | O(N) per reconcile |
| Status polling | Single Runnable | O(1) polls per interval |
| Status writes | Change detection | O(changed) per interval |
| Probe execution | Single runner pod | O(N) checks, bounded by runner resources |

## Future Scaling Considerations

### Probe Runner Horizontal Scaling

The probe runner is currently a single-replica Deployment. For thousands of probes:

- Add an HPA based on CPU/memory
- Partition the probe config across replicas (e.g., consistent hashing by probe name)
- Each replica reports results for its partition
- The StatusSyncer polls all replicas (or a results aggregator service)

### ConfigMap Size Limits

Kubernetes ConfigMaps have a 1MB limit. At ~100 bytes per probe entry, this supports ~10,000 probes. Beyond that:

- Switch to a ConfigMap-per-partition model
- Or use a different config delivery mechanism (e.g., the runner polls the API server directly)

### Informer Memory

The controller caches all HttpCanary objects in memory via the informer. At 1KB per object, 10,000 CRs = ~10MB. This is manageable but should be monitored.

For much larger scale (100K+), consider:
- Filtered informers (watch only specific namespaces or labels)
- Metadata-only informers (cache only ObjectMeta, not full Spec/Status)

package cloudflare

import (
	"context"
	"fmt"
)

// WorkerBoundDomains returns the Custom Domains currently attached to a worker
// on Cloudflare (live, via the worker's cred). This is the source of truth for
// what a worker actually serves, independent of the local record book.
func WorkerBoundDomains(ctx context.Context, workerName string) ([]WorkerDomain, error) {
	w, ok := GetWorker(workerName)
	if !ok {
		return nil, fmt.Errorf("没有这个 worker:%s", workerName)
	}
	cred, ok := GetCred(w.Cred)
	if !ok {
		return nil, fmt.Errorf("worker 绑定的凭据 %s 不存在", w.Cred)
	}
	return NewClient(cred).ListWorkerDomainsByService(ctx, w.Name)
}

// ResolveWorkerZone verifies that hostname has a matching zone under the
// worker's credential (i.e. the domain is actually bindable on Cloudflare)
// without touching any attachment. It returns the resolved zone id/name so the
// caller can bind, or an error the caller can surface before mutating anything.
func ResolveWorkerZone(ctx context.Context, workerName, hostname string) (zoneID, zoneName string, err error) {
	w, ok := GetWorker(workerName)
	if !ok {
		return "", "", fmt.Errorf("没有这个 worker:%s", workerName)
	}
	cred, ok := GetCred(w.Cred)
	if !ok {
		return "", "", fmt.Errorf("worker 绑定的凭据 %s 不存在", w.Cred)
	}
	return NewClient(cred).ResolveZoneID(ctx, hostname)
}

// ImportResult reports what ImportWorkerDomains did for one hostname.
type ImportResult struct {
	Hostname string
	Added    bool // newly inserted into the record book
	Updated  bool // already present, re-pointed to this worker / marked used
}

// ImportWorkerDomains reads a worker's live Custom Domains from Cloudflare and
// reconciles them into the record book so they become managed without the user
// typing any hostnames. Each bound hostname is:
//   - inserted under the worker's category (or "default" if it has none) and
//     marked used + attached to this worker, if not already recorded; or
//   - if already recorded, marked used and (re)attached to this worker,
//     leaving its existing category/lifecycle fields untouched.
//
// It never touches Cloudflare (read-only there) and never bans or unbinds; it
// only makes the local book reflect what the worker actually serves.
func ImportWorkerDomains(ctx context.Context, workerName string) ([]ImportResult, error) {
	w, ok := GetWorker(workerName)
	if !ok {
		return nil, fmt.Errorf("没有这个 worker:%s", workerName)
	}
	bound, err := WorkerBoundDomains(ctx, workerName)
	if err != nil {
		return nil, err
	}
	cat := w.Category
	if cat == "" {
		cat = "default"
	}
	results := make([]ImportResult, 0, len(bound))
	for _, b := range bound {
		host := b.Hostname
		if _, exists := GetDomain(host); !exists {
			if err := AddDomain(cat, host, ""); err != nil {
				// Skip malformed hostnames rather than aborting the whole import.
				continue
			}
			_ = MutateDomain(host, func(d *Domain) error {
				d.Status = StatusUsed
				d.Worker = w.Name
				return nil
			})
			results = append(results, ImportResult{Hostname: host, Added: true})
			continue
		}
		_ = MutateDomain(host, func(d *Domain) error {
			d.Status = StatusUsed
			d.Worker = w.Name
			return nil
		})
		results = append(results, ImportResult{Hostname: host, Updated: true})
	}
	return results, nil
}

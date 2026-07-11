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

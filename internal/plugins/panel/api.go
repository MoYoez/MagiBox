package panel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	cf "github.com/moyoez/magibox/internal/cloudflare"
	"github.com/moyoez/magibox/internal/config"
	kuma "github.com/moyoez/magibox/internal/kuma"
	up "github.com/moyoez/magibox/internal/uptime"
)

const apiTimeout = 25 * time.Second

// --- state ---

type credOut struct {
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
	Token     string `json:"token"` // masked
}

type workerOut struct {
	Name     string `json:"name"`
	Cred     string `json:"cred"`
	Category string `json:"category"`
	Env      string `json:"env"`
}

type domainOut struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	Sub         string `json:"sub"`
	Status      string `json:"status"`
	Worker      string `json:"worker"`
	PurchasedAt string `json:"purchased_at"`
	Usage       string `json:"usage"`
	DNS         string `json:"dns"`
	Ready       bool   `json:"ready"`
	ChangedAt   string `json:"changed_at"`
}

type kumaCredOut struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	HasKey  bool   `json:"has_key"`
}

type watcherOut struct {
	Name        string   `json:"name"`
	Target      int64    `json:"target"`
	Fields      []string `json:"fields"`
	HasTemplate bool     `json:"has_template"`
}

type failoverOut struct {
	Name      string `json:"name"`
	Worker    string `json:"worker"`
	Target    int64  `json:"target"`
	Threshold int    `json:"threshold"`
	Mode      string `json:"mode"`
	Callback  string `json:"callback"`          // full URL to paste into Kuma
	Pending   string `json:"pending,omitempty"` // domain awaiting manual apply, if any
}

func stateHandler(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}

	creds := cf.ListCreds()
	co := make([]credOut, 0, len(creds))
	for _, c := range creds {
		co = append(co, credOut{Name: c.Name, AccountID: c.AccountID, Token: mask(c.Token)})
	}
	workers := cf.ListWorkers()
	wo := make([]workerOut, 0, len(workers))
	for _, x := range workers {
		wo = append(wo, workerOut{Name: x.Name, Cred: x.Cred, Category: x.Category, Env: x.Env})
	}
	domains := cf.ListDomains("", "")
	do := make([]domainOut, 0, len(domains))
	for _, d := range domains {
		do = append(do, domainOut{
			Name: d.Name, Category: d.Category, Sub: d.Sub, Status: d.Status, Worker: d.Worker,
			PurchasedAt: d.PurchasedAt, Usage: d.Usage, DNS: d.DNS, Ready: d.Ready, ChangedAt: d.ChangedAt,
		})
	}
	rules := cf.ListFailovers()
	fo := make([]failoverOut, 0, len(rules))
	for _, r := range rules {
		pend, _ := cf.Pending(r.Name)
		fo = append(fo, failoverOut{
			Name: r.Name, Worker: r.Worker, Target: r.Target, Threshold: r.Threshold,
			Mode: string(r.Mode), Callback: failoverCallbackURL(r.Token), Pending: pend,
		})
	}
	out["cf"] = map[string]any{"creds": co, "workers": wo, "domains": do, "failovers": fo}

	kcreds := kuma.ListCreds()
	kco := make([]kumaCredOut, 0, len(kcreds))
	for _, c := range kcreds {
		kco = append(kco, kumaCredOut{Name: c.Name, BaseURL: c.BaseURL, HasKey: c.APIKey != ""})
	}
	out["kuma"] = map[string]any{"creds": kco}

	watchers := up.List()
	watch := make([]watcherOut, 0, len(watchers))
	for _, x := range watchers {
		watch = append(watch, watcherOut{Name: x.Name, Target: x.Target, Fields: x.Fields, HasTemplate: x.Template != ""})
	}
	out["uptime"] = map[string]any{"watchers": watch}

	writeJSON(w, http.StatusOK, out)
}

// --- cloudflare actions ---

func cfDomainAddHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Category, Domains, Sub string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	var ok, fail []string
	for _, d := range strings.Split(body.Domains, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if err := cf.AddDomain(body.Category, d, body.Sub); err != nil {
			fail = append(fail, d+"("+err.Error()+")")
		} else {
			ok = append(ok, d)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": ok, "failed": fail})
}

func cfDomainSetHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Domain, Field, Value string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := cf.SetDomainField(body.Domain, body.Field, body.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cfDomainDelHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Domain string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := cf.DelDomain(body.Domain); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cfBindHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Worker, Domain string
		Force          bool
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	res, err := cf.BindDomain(ctx, body.Worker, body.Domain, body.Force)
	if err != nil {
		var conf *cf.ConflictError
		if errors.As(err, &conf) {
			// Signal the conflict so the UI can offer a "force" retry.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": err.Error(), "conflict": true,
				"domain": conf.Domain, "current_worker": conf.CurrentWorker,
			})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "domain": res.Domain, "worker": res.Worker,
		"zone": res.ZoneName, "auto_picked": res.AutoPicked,
	})
}

func cfUnbindHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Domain string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	if err := cf.UnbindDomain(ctx, body.Domain); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- cloudflare failover actions ---

func cfFailoverAddHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name, Worker, Mode string
		Threshold          int
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	mode := cf.FailoverManual
	if body.Mode != "" {
		m, ok := cf.NormalizeMode(body.Mode)
		if !ok {
			writeErr(w, http.StatusBadRequest, "模式只能是 auto|manual")
			return
		}
		mode = m
	}
	rule, err := cf.AddFailover(body.Name, body.Worker, body.Threshold, mode)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "callback": failoverCallbackURL(rule.Token)})
}

func cfFailoverDelHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := cf.DelFailover(body.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cfFailoverTargetHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string
		Target int64
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := cf.MutateFailover(body.Name, func(rule *cf.FailoverRule) error {
		rule.Target = body.Target
		return nil
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cfFailoverModeHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Mode string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	mode, ok := cf.NormalizeMode(body.Mode)
	if !ok {
		writeErr(w, http.StatusBadRequest, "模式只能是 auto|manual")
		return
	}
	if err := cf.MutateFailover(body.Name, func(rule *cf.FailoverRule) error {
		rule.Mode = mode
		return nil
	}); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// cfFailoverApplyHandler executes the pending switch for a manual rule — the
// web equivalent of /cf failover apply.
func cfFailoverApplyHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	rule, ok := cf.GetFailover(body.Name)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个规则")
		return
	}
	pend, ok := cf.Pending(rule.Name)
	if !ok {
		writeErr(w, http.StatusBadRequest, "规则当前没有待确认的切换")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	res, err := cf.SwitchWorkerDomain(ctx, rule.Worker, pend)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	cf.ClearPending(rule.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bad": res.Bad, "new": res.New, "worker": res.Worker})
}

func failoverCallbackURL(token string) string {
	return strings.TrimRight(config.PublicBaseURL(), "/") + cf.FailoverHookPrefix + token
}

// --- kuma actions ---

func kumaMonitorsHandler(w http.ResponseWriter, r *http.Request) {
	cred, ok := kuma.GetCred(r.URL.Query().Get("cred"))
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个 Kuma 凭据")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	monitors, err := kuma.NewClient(cred).ListMonitors(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "monitors": monitors})
}

func kumaAddHandler(w http.ResponseWriter, r *http.Request) {
	var body struct{ Cred, Domain string }
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	cred, ok := kuma.GetCred(body.Cred)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个 Kuma 凭据")
		return
	}
	if !cf.HostValid(body.Domain) {
		writeErr(w, http.StatusBadRequest, "域名格式不对")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	id, msg, err := kuma.NewClient(cred).CreateMonitor(ctx, body.Domain, "https://"+body.Domain)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "msg": msg})
}

func kumaDelHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Cred string
		ID   int
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	cred, ok := kuma.GetCred(body.Cred)
	if !ok {
		writeErr(w, http.StatusNotFound, "没有这个 Kuma 凭据")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiTimeout)
	defer cancel()
	if err := kuma.NewClient(cred).DeleteMonitor(ctx, body.ID); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- helpers ---

func readJSON(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return sonic.Unmarshal(data, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = sonic.ConfigDefault.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func mask(v string) string {
	r := []rune(v)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:4]) + "***" + string(r[len(r)-4:])
}

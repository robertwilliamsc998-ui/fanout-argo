package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ArgoConfig is a Cloudflare Tunnel front-end for one existing fanout/Xray inbound.
// The Xray inbound still uses fanout's normal SOCKS/netns/VPN-Gate outbound.
type ArgoConfig struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	Protocol   string    `json:"protocol"`
	Hostname   string    `json:"hostname"`
	Token      string    `json:"token,omitempty"`
	Mode       string    `json:"mode"` // token | quick
	TunnelHost string    `json:"tunnel_host"`
	InboundID  int       `json:"inbound_id"`
	Port       int       `json:"port"`
	Path       string    `json:"path"`
	Link       string    `json:"link"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	Log        string    `json:"log,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type argoManager struct {
	mu     sync.Mutex
	dir    string
	items  []*ArgoConfig
	procs  map[int]*exec.Cmd
	cancel map[int]context.CancelFunc
	nextID int
}

func newArgoManager(dir string) (*argoManager, error) {
	a := &argoManager{
		dir: dir, procs: map[int]*exec.Cmd{}, cancel: map[int]context.CancelFunc{}, nextID: 1,
	}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *argoManager) file() string { return filepath.Join(a.dir, "argo.json") }

func (a *argoManager) load() error {
	b, err := os.ReadFile(a.file())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &a.items); err != nil {
		return err
	}
	for _, x := range a.items {
		if x.ID >= a.nextID {
			a.nextID = x.ID + 1
		}
		x.Status = "stopped"
	}
	return nil
}

func (a *argoManager) saveLocked() error {
	b, err := json.MarshalIndent(a.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.file())
}

func (a *argoManager) list() []*ArgoConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*ArgoConfig, 0, len(a.items))
	for _, x := range a.items {
		y := *x
		// Tunnel tokens are secrets; the UI only needs them when creating a
		// tunnel, not when listing existing Argo entries.
		y.Token = ""
		out = append(out, &y)
	}
	return out
}

func (a *argoManager) findLocked(id int) *ArgoConfig {
	for _, x := range a.items {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func cloudflaredPath(workDir string) string {
	if p := os.Getenv("FANOUT_CLOUDFLARED"); p != "" {
		return p
	}
	if p := filepath.Join(workDir, "bin", "cloudflared"); fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	return filepath.Join(workDir, "bin", "cloudflared")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func ensureCloudflared(workDir string) (string, error) {
	p := cloudflaredPath(workDir)
	if fileExists(p) {
		return p, nil
	}
	return "", fmt.Errorf("未找到 cloudflared，请运行安装脚本或手动安装到 %s", p)
}

func validHostname(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || len(h) > 253 || strings.ContainsAny(h, " /\\:@") {
		return false
	}
	return net.ParseIP(h) == nil && strings.Contains(h, ".")
}

func randomWSPath() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "/fanout"
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var s strings.Builder
	for _, v := range b {
		s.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return "/" + s.String()
}

func argoMode(token string) string {
	if strings.TrimSpace(token) == "" {
		return "quick"
	}
	return "token"
}

func argoLink(raw, hostname string) (string, error) {
	raw = strings.TrimSpace(raw)
	if hostname == "" {
		return "", fmt.Errorf("Argo 域名为空")
	}
	if strings.HasPrefix(strings.ToLower(raw), "vmess://") {
		return argoVMessLink(raw, hostname)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("原节点链接无效")
	}
	u.Host = net.JoinHostPort(hostname, "443")
	q := u.Query()
	q.Set("security", "tls")
	q.Set("sni", hostname)
	if strings.HasPrefix(q.Get("type"), "ws") || q.Get("type") == "httpupgrade" || q.Get("type") == "xhttp" {
		q.Set("host", hostname)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// argoVMessLink rewrites a VMess JSON share link. url.Parse cannot rewrite
// vmess://<base64> because the payload is not a normal URI host.
func argoVMessLink(raw, hostname string) (string, error) {
	payload := strings.TrimPrefix(strings.TrimPrefix(raw, "vmess://"), "VMESS://")
	var blob []byte
	var err error
	for _, enc := range []string{payload, payload + strings.Repeat("=", (4-len(payload)%4)%4)} {
		blob, err = base64.StdEncoding.DecodeString(enc)
		if err == nil {
			break
		}
		blob, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(enc, "="))
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("VMess 链接解码失败: %w", err)
	}
	var conf map[string]any
	if err := json.Unmarshal(blob, &conf); err != nil {
		return "", fmt.Errorf("VMess 链接解析失败: %w", err)
	}
	conf["add"] = hostname
	conf["port"] = "443"
	conf["tls"] = "tls"
	conf["sni"] = hostname
	if fmt.Sprint(orEmpty(conf["net"])) == "ws" || fmt.Sprint(orEmpty(conf["type"])) == "ws" {
		conf["host"] = hostname
	}
	fixed, err := json.Marshal(conf)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(fixed), nil
}

var quickURLRE = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func (a *argoManager) startOne(id int) error {
	a.mu.Lock()
	x := a.findLocked(id)
	if x == nil {
		a.mu.Unlock()
		return fmt.Errorf("Argo %d 不存在", id)
	}
	if _, ok := a.procs[id]; ok {
		a.mu.Unlock()
		return nil
	}
	cfg := *x
	a.mu.Unlock()

	bin, err := ensureCloudflared(a.dir)
	if err != nil {
		a.setStatus(id, "failed", err.Error(), "")
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	var cmd *exec.Cmd
	if cfg.Mode == "quick" {
		// Quick Tunnels read ~/.cloudflared/config.yml when present. Give each
		// instance an isolated HOME so an unrelated cloudflared config cannot
		// break the temporary tunnel.
		home := filepath.Join(a.dir, "argo-home", fmt.Sprint(cfg.ID))
		if err := os.MkdirAll(home, 0700); err != nil {
			cancel()
			return fmt.Errorf("创建 Quick Tunnel 运行目录失败: %w", err)
		}
		cmd = exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "--url", fmt.Sprintf("http://127.0.0.1:%d", cfg.Port))
		cmd.Env = append(os.Environ(), "HOME="+home)
	} else {
		if strings.TrimSpace(cfg.Token) == "" {
			cancel()
			return fmt.Errorf("固定 Argo 缺少 Tunnel Token")
		}
		// TUNNEL_TOKEN is supported for remotely-managed tunnels and avoids
		// exposing the token in `ps` output.
		cmd = exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "run")
		cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+cfg.Token)
	}

	logPath := filepath.Join(a.dir, fmt.Sprintf("argo-%d.log", id))
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		cancel()
		return err
	}
	cmd.Stdout = logf
	cmd.Stderr = logf

	if err := cmd.Start(); err != nil {
		logf.Close()
		cancel()
		a.setStatus(id, "failed", err.Error(), "")
		return err
	}

	a.mu.Lock()
	a.procs[id] = cmd
	a.cancel[id] = cancel
	x = a.findLocked(id)
	if x != nil {
		x.Status = "starting"
		x.Error = ""
		x.Log = logPath
	}
	_ = a.saveLocked()
	a.mu.Unlock()

	go func() {
		defer logf.Close()
		err := cmd.Wait()
		cancel()
		shouldRestart := false
		a.mu.Lock()
		delete(a.procs, id)
		delete(a.cancel, id)
		x := a.findLocked(id)
		if x != nil && x.Status != "stopped" {
			shouldRestart = true
			x.Status = "stopped"
			if err != nil {
				x.Error = err.Error()
			}
		}
		_ = a.saveLocked()
		a.mu.Unlock()
		if shouldRestart {
			// A tunnel process can disappear because of a transient network or
			// Cloudflare error. Keep the Argo definition desired/running and
			// retry without requiring the user to press Start again.
			time.Sleep(5 * time.Second)
			if err := a.startOne(id); err != nil {
				logf("Argo %d 自动重启失败: %v", id, err)
			}
		}
	}()

	if cfg.Mode == "quick" {
		if err := a.waitQuickHostname(id, logPath, cfg.Port); err != nil {
			_ = a.stop(id)
			return err
		}
	} else {
		a.setStatus(id, "up", "", logPath)
	}
	return nil
}

func (a *argoManager) waitQuickHostname(id int, logPath string, port int) error {
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		if m := quickURLRE.FindString(string(b)); m != "" {
			a.mu.Lock()
			x := a.findLocked(id)
			if x != nil {
				host := strings.TrimPrefix(m, "https://")
				x.Hostname = host
				// The stored Link is regenerated after the process exposes its URL.
				if p, err := openPanel(); err == nil {
					if d, err := p.InboundDetail(x.InboundID, host); err == nil && len(d.Links) > 0 {
						if link, err := argoLink(d.Links[0], host); err == nil {
							x.Link = link
						}
					}
				}
				x.Status = "up"
				x.Error = ""
				_ = a.saveLocked()
			}
			a.mu.Unlock()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("Quick Tunnel 启动超时，详见 %s", logPath)
}

func (a *argoManager) setStatus(id int, status, errText, logPath string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if x := a.findLocked(id); x != nil {
		x.Status = status
		x.Error = errText
		if logPath != "" {
			x.Log = logPath
		}
	}
	_ = a.saveLocked()
}

func (a *argoManager) stop(id int) error {
	a.mu.Lock()
	cancel := a.cancel[id]
	x := a.findLocked(id)
	if x != nil {
		x.Status = "stopped"
	}
	delete(a.cancel, id)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.mu.Lock()
	_ = a.saveLocked()
	a.mu.Unlock()
	return nil
}

func (a *argoManager) startAll() {
	for _, x := range a.list() {
		id := x.ID
		go func() {
			if err := a.startOne(id); err != nil {
				logf("Argo %d 启动失败: %v", id, err)
			}
		}()
	}
}

func (a *argoManager) create(m *Manager, req ArgoCreateRequest) (*ArgoConfig, error) {
	proto := strings.ToLower(strings.TrimSpace(req.Protocol))
	if proto != "vless" && proto != "vmess" {
		return nil, fmt.Errorf("Argo 协议只支持 vless 或 vmess")
	}
	host := strings.TrimSpace(req.TunnelHost)
	if host == "" {
		return nil, fmt.Errorf("必须选择一个 fanout 出口")
	}
	var target *Tunnel
	for _, t := range m.Tunnels() {
		if t.Node.HostName == host && t.Status == "up" {
			target = t
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("出口 %s 当前没有连通", host)
	}

	mode := argoMode(req.Token)
	hostname := strings.TrimSpace(req.Hostname)
	if mode == "token" && !validHostname(hostname) {
		return nil, fmt.Errorf("固定 Argo 模式必须填写有效域名，例如 argo.example.com")
	}

	path := strings.TrimSpace(req.Path)
	if path == "" {
		path = randomWSPath()
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	p, err := openPanel()
	if err != nil {
		return nil, err
	}

	spec := NewInboundSpec{
		Protocol: proto,
		Network:  "ws",
		Security: "none",
		Path:     path,
		Remark:   "Argo-" + proto,
	}
	created, err := p.CreateInbound(spec, m.Tunnels())
	if err != nil {
		return nil, err
	}

	tag := fmt.Sprintf("in-%d-ws", created.Port)
	if err := p.Bind(tag, host, m.Tunnels()); err != nil {
		_ = p.DeleteInbounds([]int{created.ID}, m.Tunnels())
		return nil, err
	}

	a.mu.Lock()
	x := &ArgoConfig{
		ID: a.nextID, Name: fmt.Sprintf("Argo-%s-%d", proto, created.Port),
		Protocol: proto, Hostname: hostname, Token: strings.TrimSpace(req.Token),
		Mode: mode, TunnelHost: host, InboundID: created.ID, Port: created.Port,
		Path: path, Status: "stopped", CreatedAt: time.Now(),
	}
	a.nextID++
	a.items = append(a.items, x)
	err = a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		_ = p.DeleteInbounds([]int{created.ID}, m.Tunnels())
		return nil, err
	}

	if mode == "token" {
		if d, e := p.InboundDetail(created.ID, hostname); e == nil && len(d.Links) > 0 {
			x.Link, _ = argoLink(d.Links[0], hostname)
		}
	} else {
		// Quick Tunnel hostname is only known after cloudflared starts.
	}

	if err := a.startOne(x.ID); err != nil {
		// Keep the Argo definition and Xray inbound so the user can fix/restart it.
		return x, err
	}
	return x, nil
}

func (a *argoManager) delete(m *Manager, id int) error {
	a.mu.Lock()
	x := a.findLocked(id)
	if x == nil {
		a.mu.Unlock()
		return fmt.Errorf("Argo %d 不存在", id)
	}
	inboundID := x.InboundID
	a.mu.Unlock()

	_ = a.stop(id)
	p, err := openPanel()
	if err != nil {
		return err
	}
	if err := p.DeleteInbounds([]int{inboundID}, m.Tunnels()); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	var keep []*ArgoConfig
	for _, v := range a.items {
		if v.ID != id {
			keep = append(keep, v)
		}
	}
	a.items = keep
	return a.saveLocked()
}

func (a *argoManager) close() {
	for _, x := range a.list() {
		_ = a.stop(x.ID)
	}
}

// ArgoCreateRequest is intentionally small: the fanout exit remains the only
// outbound mechanism; Argo only supplies the public ingress.
type ArgoCreateRequest struct {
	Protocol   string `json:"protocol"`
	TunnelHost string `json:"tunnel_host"`
	Hostname   string `json:"hostname"`
	Token      string `json:"token"`
	Path       string `json:"path"`
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

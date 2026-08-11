package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Argo struct {
	ID        int       `json:"id"`
	Protocol  string    `json:"protocol"`
	Hostname  string    `json:"hostname"`
	Path      string    `json:"path"`
	LocalPort int       `json:"local_port"`
	InboundID int       `json:"inbound_id"`
	ExitHost  string    `json:"exit_host"`
	Mode      string    `json:"mode"` // quick | fixed
	Token     string    `json:"token,omitempty"`
	Status    string    `json:"status"`
	Disabled  bool      `json:"disabled,omitempty"`
	Link      string    `json:"link"`
	Error     string    `json:"error,omitempty"`
	PID       int       `json:"pid,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	clientID  string    `json:"-"`
}

type argoStore struct {
	NextID int     `json:"next_id"`
	Argo   []*Argo `json:"argo"`
}

type ArgoManager struct {
	mu    sync.Mutex
	dir   string
	mgr   *Manager
	panel Panel
	store *argoStore
	procs map[int]*exec.Cmd
}

func newArgoManager(dir string, mgr *Manager, panel Panel) (*ArgoManager, error) {
	if dir == "" {
		return nil, errors.New("argo 工作目录为空")
	}
	p := filepath.Join(dir, "argo.json")
	st := &argoStore{NextID: 1}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", p, err)
		}
		if st.NextID < 1 {
			st.NextID = 1
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return &ArgoManager{dir: dir, mgr: mgr, panel: panel, store: st, procs: map[int]*exec.Cmd{}}, nil
}

func (a *ArgoManager) saveLocked() error {
	b, err := json.MarshalIndent(a.store, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(a.dir, "argo.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
func dirForArgo(base string, id int) string { return filepath.Join(base, "argo", fmt.Sprint(id)) }
func (a *ArgoManager) binary() string {
	for _, p := range []string{filepath.Join(a.dir, "bin", "cloudflared"), "/usr/local/bin/cloudflared", "/usr/bin/cloudflared"} {
		if st, e := os.Stat(p); e == nil && st.Mode()&0111 != 0 {
			return p
		}
	}
	return "cloudflared"
}
func (a *ArgoManager) list() []*Argo {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*Argo, 0, len(a.store.Argo))
	for _, x := range a.store.Argo {
		y := *x
		y.Token = ""
		y.PID = 0
		out = append(out, &y)
	}
	return out
}
func (a *ArgoManager) find(id int) *Argo {
	for _, x := range a.store.Argo {
		if x.ID == id {
			return x
		}
	}
	return nil
}

func (a *ArgoManager) Create(protocol, mode, hostname, token, exitHost string) (*Argo, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "vless" && protocol != "vmess" {
		return nil, errors.New("协议只能是 vless 或 vmess")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "quick" && mode != "fixed" {
		return nil, errors.New("Argo 模式只能是 quick 或 fixed")
	}
	if mode == "fixed" && (hostname == "" || token == "") {
		return nil, errors.New("固定 Tunnel 必须提供域名和 Tunnel Token")
	}
	if mode == "quick" {
		hostname = ""
		token = ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.panel == nil {
		return nil, errors.New("Xray 后端不可用")
	}
	path := "/" + randomHex(8)
	remark := fmt.Sprintf("Argo-%s-%d", strings.ToUpper(protocol), a.store.NextID)
	ib, err := a.panel.CreateInbound(NewInboundSpec{Protocol: protocol, Network: "ws", Security: "none", Path: path, Remark: remark}, a.mgr.Tunnels())
	if err != nil {
		return nil, err
	}
	detail, err := a.panel.InboundDetail(ib.ID, "")
	if err != nil {
		_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
		return nil, err
	}
	if len(detail.Clients) == 0 {
		_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
		return nil, errors.New("新建入站没有客户端")
	}
	inbTag := detail.Tag
	if exitHost != "" {
		if err := a.panel.Bind(inbTag, exitHost, a.mgr.Tunnels()); err != nil {
			_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
			return nil, err
		}
	} else {
		_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
		return nil, errors.New("必须指定 fanout 出口")
	}
	x := &Argo{ID: a.store.NextID, Protocol: protocol, Hostname: hostname, Path: path, LocalPort: ib.Port, InboundID: ib.ID, ExitHost: exitHost, Mode: mode, Token: token, Status: "starting", Disabled: false, CreatedAt: time.Now()}
	a.store.NextID++
	a.store.Argo = append(a.store.Argo, x)
	if err := a.refreshLinkLocked(x); err != nil {
		_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
		a.store.Argo = a.store.Argo[:len(a.store.Argo)-1]
		a.store.NextID--
		return nil, err
	}
	if err := a.saveLocked(); err != nil {
		_ = a.panel.DeleteInbounds([]int{ib.ID}, a.mgr.Tunnels())
		a.store.Argo = a.store.Argo[:len(a.store.Argo)-1]
		a.store.NextID--
		return nil, err
	}
	a.writeInfoLocked()
	if err := a.startLocked(x); err != nil {
		x.Status = "failed"
		x.Error = err.Error()
		_ = a.saveLocked()
		return nil, err
	}
	return a.publicCopyLocked(x), nil
}

func (a *ArgoManager) writeInfoLocked() {
	var b strings.Builder
	b.WriteString("fanout Argo 节点\n")
	b.WriteString("==============================\n")
	for _, x := range a.store.Argo {
		if x.Link == "" {
			continue
		}
		fmt.Fprintf(&b, "[%d] %s-%s\n", x.ID, strings.ToUpper(x.Protocol), "Argo")
		fmt.Fprintf(&b, "出口: %s\n", x.ExitHost)
		fmt.Fprintf(&b, "域名: %s\n", x.Hostname)
		fmt.Fprintf(&b, "状态: %s\n", x.Status)
		fmt.Fprintf(&b, "%s\n\n", x.Link)
	}
	_ = os.WriteFile("/root/info.txt", []byte(b.String()), 0600)
}

func (a *ArgoManager) publicCopyLocked(x *Argo) *Argo { y := *x; y.Token = ""; y.PID = 0; return &y }

func (a *ArgoManager) startLocked(x *Argo) error {
	if p := a.procs[x.ID]; p != nil && p.Process != nil {
		return nil
	}
	bin := a.binary()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("找不到 cloudflared：%v", err)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d", x.LocalPort)
	dir := filepath.Join(a.dir, "argo", fmt.Sprint(x.ID))
	_ = os.MkdirAll(dir, 0700)
	var cmd *exec.Cmd
	if x.Mode == "fixed" {
		tokenFile := filepath.Join(dirForArgo(a.dir, x.ID), "token")
		if err := os.WriteFile(tokenFile, []byte(strings.TrimSpace(x.Token)+"\n"), 0600); err != nil {
			return err
		}
		cmd = exec.Command(bin, "tunnel", "--no-autoupdate", "run", "--token-file", tokenFile)
		cmd.Env = append(os.Environ(), "HOME="+filepath.Join(a.dir, "argo", fmt.Sprint(x.ID)))
	} else {
		cmd = exec.Command(bin, "tunnel", "--no-autoupdate", "--url", target)
		cmd.Env = append(os.Environ(), "HOME="+filepath.Join(a.dir, "argo", fmt.Sprint(x.ID)))
	}
	logp := filepath.Join(dir, "cloudflared.log")
	f, err := os.OpenFile(logp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	x.PID = cmd.Process.Pid
	a.procs[x.ID] = cmd
	x.Status = "up"
	x.Error = ""
	if x.Mode == "quick" {
		go a.watchProcess(x.ID, f)
	} else {
		go a.watchProcess(x.ID, f)
	}
	_ = a.saveLocked()
	if x.Mode == "quick" {
		go a.waitQuickHostname(x.ID, logp)
	}
	return nil
}

func (a *ArgoManager) watchProcess(id int, f *os.File) {
	a.mu.Lock()
	p := a.procs[id]
	a.mu.Unlock()
	if p == nil {
		return
	}
	err := p.Wait()
	f.Close()
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.procs, id)
	x := a.find(id)
	if x == nil {
		return
	}
	x.PID = 0
	if x.Status != "stopped" {
		x.Status = "down"
		if err != nil {
			x.Error = err.Error()
		}
		_ = a.saveLocked()
		go func() {
			time.Sleep(5 * time.Second)
			a.mu.Lock()
			defer a.mu.Unlock()
			if cur := a.find(id); cur != nil && cur.Status != "stopped" {
				cur.Status = "starting"
				if e := a.startLocked(cur); e != nil {
					cur.Status = "down"
					cur.Error = e.Error()
					_ = a.saveLocked()
				}
			}
		}()
	}
}

var tryCloudflareRE = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func (a *ArgoManager) waitQuickHostname(id int, logp string) {
	for i := 0; i < 30; i++ {
		time.Sleep(time.Second)
		b, e := os.ReadFile(logp)
		if e != nil {
			continue
		}
		m := tryCloudflareRE.FindString(string(b))
		if m != "" {
			a.mu.Lock()
			x := a.find(id)
			if x != nil && x.Hostname == "" {
				x.Hostname = strings.TrimPrefix(m, "https://")
				x.Link = argoLink(x)
				x.Status = "up"
				a.writeInfoLocked()
				_ = a.saveLocked()
			}
			a.mu.Unlock()
			return
		}
	}
}

func (a *ArgoManager) Start(id int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	x := a.find(id)
	if x == nil {
		return errors.New("Argo 不存在")
	}
	return a.startLocked(x)
}
func (a *ArgoManager) Stop(id int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	x := a.find(id)
	if x == nil {
		return errors.New("Argo 不存在")
	}
	x.Status = "stopped"
	x.Disabled = true
	if p := a.procs[id]; p != nil && p.Process != nil {
		_ = p.Process.Signal(syscall.SIGTERM)
		delete(a.procs, id)
	}
	x.PID = 0
	err := a.saveLocked()
	a.writeInfoLocked()
	return err
}
func (a *ArgoManager) Delete(id int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	x := a.find(id)
	if x == nil {
		return errors.New("Argo 不存在")
	}
	if p := a.procs[id]; p != nil && p.Process != nil {
		_ = p.Process.Signal(syscall.SIGTERM)
		delete(a.procs, id)
	}
	if err := a.panel.DeleteInbounds([]int{x.InboundID}, a.mgr.Tunnels()); err != nil {
		return err
	}
	for i, v := range a.store.Argo {
		if v.ID == id {
			a.store.Argo = append(a.store.Argo[:i], a.store.Argo[i+1:]...)
			break
		}
	}
	_ = os.RemoveAll(filepath.Join(a.dir, "argo", fmt.Sprint(id)))
	err := a.saveLocked()
	a.writeInfoLocked()
	return err
}

func (a *ArgoManager) Restore() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, x := range a.store.Argo {
		if x.Disabled {
			x.Status = "stopped"
			continue
		}
		if x.Mode == "quick" {
			x.Hostname = ""
			x.Link = ""
		}
		x.Status = "starting"
		_ = a.startLocked(x)
	}
}
func (a *ArgoManager) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, p := range a.procs {
		if p != nil && p.Process != nil {
			_ = p.Process.Signal(syscall.SIGTERM)
		}
		delete(a.procs, id)
	}
}

func argoLink(x *Argo) string {
	host := x.Hostname
	if host == "" {
		return ""
	}
	q := url.Values{}
	q.Set("type", "ws")
	q.Set("security", "tls")
	q.Set("path", x.Path)
	q.Set("host", host)
	q.Set("sni", host)
	if x.Protocol == "vless" {
		q.Set("encryption", "none")
		return fmt.Sprintf("vless://%s@%s:443?%s#%s", argoClientID(x), host, q.Encode(), url.PathEscape(x.Protocol+"-Argo"))
	}
	conf := map[string]any{"v": "2", "ps": x.Protocol + "-Argo", "add": host, "port": "443", "id": argoClientID(x), "aid": "0", "scy": "auto", "net": "ws", "type": "none", "host": host, "path": x.Path, "tls": "tls", "sni": host, "alpn": "h2,http/1.1"}
	b, _ := json.Marshal(conf)
	return "vmess://" + base64.RawStdEncoding.EncodeToString(b)
}
func argoClientID(x *Argo) string { return x.clientID }

// clientID is populated from the inbound detail after creation. It is kept separately
// from the persisted public object to avoid changing the on-disk format.
func (a *ArgoManager) refreshLinkLocked(x *Argo) error {
	d, err := a.panel.InboundDetail(x.InboundID, "")
	if err != nil {
		return err
	}
	if len(d.Clients) == 0 {
		return errors.New("入站没有客户端")
	}
	x.clientID = d.Clients[0].ID
	x.Link = argoLink(x)
	return nil
}

// argoClientID fallback storage. JSON omits it to keep compatibility and avoid exposing it in list output.

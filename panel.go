package main

import (
	"fmt"
	"os"
	"sync"
)

// Panel 是 fanout 管理节点链接的后端。
//
// 有两个实现：接管本机 3x-ui 面板的 XUI，以及 fanout 自己跑 Xray 的 Native。
// 界面和编排层只依赖这个接口，两种模式下的操作语义完全一致。
type Panel interface {
	// Kind 返回 "3x-ui" 或 "native"，界面据此提示当前模式。
	Kind() string
	// Describe 给出一行人能读的后端说明。
	Describe() string

	Inbounds(live map[string]bool) ([]Inbound, error)
	InboundDetail(id int, publicHost string) (*InboundDetail, error)
	InboundLinks(ids []int, publicHost string) ([]string, error)

	Bind(inboundTag string, hostname string, tunnels []*Tunnel) error
	Rebind(oldHost string, target *Tunnel, tunnels []*Tunnel) error
	ResyncOutbound(t *Tunnel, tunnels []*Tunnel) error

	CloneToTunnels(templateID int, hosts []string, tunnels []*Tunnel) ([]int, error)
	DeleteInbounds(ids []int, tunnels []*Tunnel) error

	// CreateInbound 新建一个入站。自建模式写自己的库并重建 Xray 配置，
	// 接管 3x-ui 时走面板的 inbounds/add API，让面板照常管这条入站。
	CreateInbound(spec NewInboundSpec, tunnels []*Tunnel) (*CreatedInbound, error)

	// UpdateInbound 改端口、备注与启停。只有非零/非 nil 的字段会被写入。
	UpdateInbound(id int, patch InboundPatch, tunnels []*Tunnel) error

	// AddClient 给入站加一个客户端，email 留空时自动命名。
	AddClient(id int, email string, tunnels []*Tunnel) error
	// DeleteClient 摘掉入站上的一个客户端。
	DeleteClient(id int, email string, tunnels []*Tunnel) error
	// ResetClient 换掉客户端的凭据（UUID / trojan 密码），已分发的旧链接随即失效。
	ResetClient(id int, email string, tunnels []*Tunnel) error

	// OnTunnelsChanged 在隧道集合变化后调用。
	//
	// 自建模式的出站完全由隧道列表推导，新开的出口必须重建配置才有对应出站；
	// 接管 3x-ui 时出站在 Bind/Clone 里顺带同步，这里是空操作，
	// 免得每开一条隧道就白重启一次面板的 Xray。
	OnTunnelsChanged(tunnels []*Tunnel) error

	// Close 释放后端占用的资源。自建模式要停掉自己拉起的 Xray，
	// 否则 fanout 退出后它会变成孤儿进程，下次启动撞端口。
	Close()
}

// InboundPatch 描述对入站的一次局部修改。指针为 nil 表示该字段不动。
type InboundPatch struct {
	Port   *int
	Remark *string
	Enable *bool
}

// CreatedInbound 是新建入站后回给界面的摘要。
type CreatedInbound struct {
	ID       int    `json:"id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Remark   string `json:"remark"`
	Network  string `json:"network"`
	Security string `json:"security"`
}

// closePanel 在进程退出时释放后端资源。
func closePanel() {
	panelState.mu.Lock()
	p := panelState.current
	panelState.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// panelState 缓存已选定的后端。探测涉及执行 x-ui 命令，没必要每个请求都做一次。
var panelState struct {
	mu      sync.Mutex
	current Panel
	workDir string
	forced  string
}

// configurePanel 记录自建模式需要的工作目录与用户指定的模式。
// mode 为空表示自动探测，也可以是 "3x-ui" 或 "native"。
func configurePanel(workDir, mode string) {
	panelState.mu.Lock()
	defer panelState.mu.Unlock()
	panelState.workDir = workDir
	panelState.forced = mode
	panelState.current = nil
}

// openPanel 返回当前可用的后端。
//
// 优先接管本机已装的 3x-ui：用户既然装了面板，入站大概率在那边管着，
// fanout 另起一个 Xray 会和面板抢端口。探测不到才用自建模式。
func openPanel() (Panel, error) {
	panelState.mu.Lock()
	defer panelState.mu.Unlock()

	if panelState.current != nil {
		return panelState.current, nil
	}

	switch panelState.forced {
	case "3x-ui":
		x, err := DetectXUI(panelState.workDir)
		if err != nil {
			return nil, fmt.Errorf("指定了 3x-ui 模式但探测失败: %w", err)
		}
		panelState.current = x
		return x, nil
	case "native":
		n, err := openNative(panelState.workDir)
		if err != nil {
			return nil, err
		}
		panelState.current = n
		return n, nil
	}

	if x, err := DetectXUI(panelState.workDir); err == nil {
		panelState.current = x
		return x, nil
	} else if !xuiAbsent() {
		// 面板装了却读不出配置，这时自建模式会和它抢端口，宁可报错让用户看见
		return nil, fmt.Errorf("检测到 3x-ui 但读取配置失败: %w", err)
	}

	n, err := openNative(panelState.workDir)
	if err != nil {
		return nil, err
	}
	panelState.current = n
	return n, nil
}

// xuiAbsent 判断本机是否根本没装 3x-ui。
func xuiAbsent() bool {
	if _, err := os.Stat(xuiBinary); err == nil {
		return false
	}
	if _, err := os.Stat(xuiMenu); err == nil {
		return false
	}
	return true
}

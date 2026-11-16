package nsjail

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/fumin/httproxy/util"
	"github.com/fumin/httproxy/util/nsjail/dockerseccomp"
	"github.com/fumin/httproxy/util/nsjail/nsjailpb"
)

func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	cfg := defaultConfig()
	return run(ctx, cfg, stdin, stdout, stderr)
}

func run(ctx context.Context, cfg *nsjailpb.NsJailConfig, stdin io.Reader, stdout, stderr io.Writer) error {
	cfgText, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	cfgPath, err := util.WriteTemp("nsjail-*.conf", cfgText)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer os.Remove(cfgPath)

	const nsjailBin = "/usr/local/bin/nsjail"
	cmd := exec.CommandContext(ctx,
		"systemd-run", "--user", "--scope", "--quiet", "--collect",
		nsjailBin, "--config", cfgPath)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Change Cancel from the default SIGKILL to SIGTERM, so that nsjail
	// has the chance to cleanup itself like clearing it's cgroup.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	// Send SIGKILL if nsjail could not cleanup itself in time.
	cmd.WaitDelay = 5 * time.Second
	// Kill child if we die before canceling ctx, such as by SIGKILL.
	// Pdeathsig is sent when the thread that started the child exits,
	// not the process, so pin this goroutine to its thread to prevent the
	// thread from being recycled by the Go runtime, resulting in Pdeathsig
	// being sent prematurely.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// We were asked to stop, and nsjail has stopped.
			return nil
		}
		return errors.Wrap(err, strings.Join(cmd.Args, " "))
	}
	return nil
}

func defaultConfig() *nsjailpb.NsJailConfig {
	type (
		rule   = nsjailpb.NsJailConfig_TrafficRule
		family = nsjailpb.NsJailConfig_TrafficRule_IpFamily
	)
	const (
		allow  = nsjailpb.NsJailConfig_TrafficRule_ALLOW
		reject = nsjailpb.NsJailConfig_TrafficRule_REJECT
		ipv4   = nsjailpb.NsJailConfig_TrafficRule_IPV4
		ipv6   = nsjailpb.NsJailConfig_TrafficRule_IPV6
	)

	// Block all local area networks.
	rules := make([]*rule, 0)
	for _, ip := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16"} {
		rules = append(rules, &rule{Action: reject.Enum(), DstIp: new(ip)})
	}
	for _, ip := range []string{"fc00::/7", "fe80::/10"} {
		rules = append(rules, &rule{Action: reject.Enum(), DstIp: new(ip), IpFamily: ipv6.Enum()})
	}

	const (
		ip4 = "10.255.255.2"
		gw4 = "10.255.255.1"
		ip6 = "fc00::2"
		gw6 = "fc00::1"
	)

	// Gateway traffic rules.
	gateways := []struct {
		ip     string
		family family
		icmp   nsjailpb.NsJailConfig_TrafficRule_Protocol
	}{
		{ip: gw4 + "/32", family: ipv4, icmp: nsjailpb.NsJailConfig_TrafficRule_ICMP},
		{ip: gw6 + "/128", family: ipv6, icmp: nsjailpb.NsJailConfig_TrafficRule_ICMPV6},
	}
	for _, gw := range gateways {
		// For unknown protocols, ALLOW.
		// This tells the kernel that the gateway is reachable, so that external traffic can pass through.
		// This rule has to come *before* the below REJECT rules, since later rules have higher priority.
		rules = append(rules, &rule{Action: allow.Enum(), DstIp: new(gw.ip), IpFamily: gw.family.Enum()})
		// For actual tcp, udp, and icmp traffic to gateway, REJECT.
		// Since nstun forwards only TCP, UDP and ICMP, this means every packet to the gateway's IP is blocked.
		// Packets to the external internet are still accepted, since they are only routed via the gateway, while their destination IP is some other address.
		for _, p := range []nsjailpb.NsJailConfig_TrafficRule_Protocol{nsjailpb.NsJailConfig_TrafficRule_TCP, nsjailpb.NsJailConfig_TrafficRule_UDP, gw.icmp} {
			rules = append(rules, &rule{Action: reject.Enum(), DstIp: new(gw.ip), Proto: p.Enum(), IpFamily: gw.family.Enum()})
		}
	}

	mounts := []*nsjailpb.MountPt{{
		SrcContent: []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"),
		Dst:        new("/etc/resolv.conf"),
	}}
	binds := []string{
		"/lib/x86_64-linux-gnu",
		"/lib64",
		"/bin",
		"/usr/bin",
		"/lib/cargo/bin",
		"/etc/alternatives",
		"/dev/urandom",
		"/dev/null",
		"/etc/ssl/certs",
		// Terminal descriptions for drawing on TERM. Needed for vim.
		"/usr/share/terminfo",
		"/usr/share/vim",
		"/etc/vim",
	}
	for _, p := range binds {
		mounts = append(mounts, &nsjailpb.MountPt{Src: new(p), Dst: new(p), IsBind: new(true)})
	}
	mounts = append(mounts, &nsjailpb.MountPt{Dst: new("/tmp"), Rw: new(true), Fstype: new("tmpfs")})

	uid := os.Getuid()
	cgroupv2Mount := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service", uid, uid)

	return &nsjailpb.NsJailConfig{
		Name:        new("bash-jail"),
		Mode:        nsjailpb.Mode_ONCE.Enum(),
		CloneNewnet: new(true),
		UserNet: &nsjailpb.NsJailConfig_UserNet{
			Backend: nsjailpb.NsJailConfig_UserNet_NSTUN.Enum(),
			Ip4:     proto.String(ip4),
			Gw4:     proto.String(gw4),
			Ip6:     proto.String(ip6),
			Gw6:     proto.String(gw6),
		},
		TrafficRule: rules,
		Mount:       mounts,
		MountProc:   new(true),

		// Resource limits.
		UseCgroupv2:       new(true),
		Cgroupv2Mount:     new(cgroupv2Mount),
		CgroupMemMax:      proto.Uint64(64 << 20),
		CgroupMemSwapMax:  proto.Int64(0),
		CgroupCpuMsPerSec: proto.Uint32(500),
		CgroupPidsMax:     proto.Uint64(256),
		RlimitNofile:      proto.Uint64(64),

		TimeLimit:       proto.Uint32(0),
		RlimitCpuType:   nsjailpb.RLimit_INF.Enum(),
		RlimitFsizeType: nsjailpb.RLimit_INF.Enum(),

		SeccompString: []string{dockerseccomp.Policy},

		ExecBin: &nsjailpb.Exe{Path: new("/bin/bash")},
	}
}

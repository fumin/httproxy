// The layers, outermost first:
//   - systemd-run --user --scope: a cgroup with memory, CPU and task limits for everything below.
//   - pasta: a private user and network namespace with internet access. Port forwarding and the
//     gateway-to-host-loopback mapping are disabled, so the sandbox cannot reach the host's
//     localhost.
//   - prohibit routes in pasta's namespace for the blocked ranges and the gateway. pasta's
//     namespace forwards the sandbox's traffic, so the kernel applies these routes to every packet
//     the sandbox sends. IPv4 connections to blocked ranges fail immediately with "No route to
//     host". IPv6 connections are blocked too, but hang until the caller's timeout, since gVisor
//     ignores the ICMPv6 "administratively prohibited" errors that the kernel sends back.
//   - an inner network namespace, linked to pasta's by a veth pair, for runsc. Since the firewall
//     is in a different network namespace, neither the sandboxed processes nor a process that
//     escapes gVisor into the Sentry can change it. The Sentry also runs in a user namespace nested
//     inside pasta's, so it has no capabilities over either network namespace.
//   - runsc --network=sandbox run: gVisor with its own network stack on the inner namespace's veth,
//     started from an OCI bundle with a minimal root filesystem plus read-only binds of the allowed
//     host paths. Writes to the root filesystem go to an in-memory overlay.
package gvisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"

	"github.com/fumin/httproxy/util"
)

const (
	// Binaries used.
	pastaBin   = "/usr/bin/pasta"
	runscBin   = "/usr/bin/runsc"
	ipBin      = "/usr/bin/ip"
	unshareBin = "/usr/bin/unshare"
	nsenterBin = "/usr/bin/nsenter"

	// Settings in pasta's network namespace.
	nsIfname = "eth0"
	nsAddr4  = "10.255.255.2"
	nsGw4    = "10.255.255.1"
	nsAddr6  = "fc00::2"
	nsGw6    = "fc00::1"

	// Settings for the veth network interface pair between pasta's network namespace (out) and runsc's (in).
	vethOut      = "vout"
	vethIn       = "vin"
	vethOutAddr4 = "10.255.254.1"
	vethInAddr4  = "10.255.254.2"
	vethOutAddr6 = "fd00::1"
	vethInAddr6  = "fd00::2"
	// File descriptors through which the inner script reaches pasta's network namespace and its own.
	pastaNsFd = 3
	innerNsFd = 4
)

type config struct {
	// binds are host paths visible read-only at the same path inside the sandbox.
	binds      []string
	resolvConf string
	// blocked are destination ranges the sandbox cannot connect to, in addition to the gateway.
	blocked []string

	memoryMax uint64
	// cpuQuota is the percentage of one CPU.
	cpuQuota int
	tasksMax int
	nofile   int

	execBin string
	env     []string
}

func defaultConfig() config {
	return config{
		binds: []string{
			"/lib/x86_64-linux-gnu",
			"/lib64",
			"/bin",
			"/usr/bin",
			"/lib/cargo/bin",
			"/etc/alternatives",
			"/etc/ssl/certs",
			// Terminal descriptions for drawing on TERM. Needed for vim.
			"/usr/share/terminfo",
			"/usr/share/vim",
			"/etc/vim",
		},
		resolvConf: "nameserver 1.1.1.1\nnameserver 8.8.8.8\n",
		// Local area networks.
		blocked: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fc00::/7", "fe80::/10"},

		memoryMax: 64 << 20,
		cpuQuota:  50,
		tasksMax:  256,
		nofile:    64,

		execBin: "/bin/bash",
		env:     []string{"PATH=/usr/bin:/bin"},
	}
}

func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	return run(ctx, defaultConfig(), stdin, stdout, stderr)
}

func run(ctx context.Context, cfg config, stdin io.Reader, stdout, stderr io.Writer) error {
	id := "gvisor-" + util.RandStr()
	dir, err := os.MkdirTemp("", id)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer os.RemoveAll(dir)

	runscArgs, err := makeRunscArgs(id, dir, cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	pastaArgs, err := makePastaArgs(cfg.blocked, runscArgs)
	if err != nil {
		return errors.Wrap(err, "")
	}
	unit := id + ".scope"
	args := []string{
		"systemd-run", "--user", "--scope", "--quiet", "--collect",
		// Pass the command through unchanged, instead of expanding $VAR in it.
		"--expand-environment=no",
		"--unit=" + unit,
		"-p", fmt.Sprintf("MemoryMax=%d", cfg.memoryMax),
		"-p", "MemorySwapMax=0",
		"-p", fmt.Sprintf("CPUQuota=%d%%", cfg.cpuQuota),
		"-p", fmt.Sprintf("TasksMax=%d", cfg.tasksMax),
		"-p", "TimeoutStopSec=5s",
	}
	args = append(args, pastaArgs...)

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Cancel = func() error {
		if err := stopUnit(unit); err != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	// Kill child if we die before canceling ctx, such as by SIGKILL.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Killing pasta leaves runsc running, so always stop the whole scope.
	defer stopUnit(unit)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// We were asked to stop, and the sandbox has stopped.
			return nil
		}
		return errors.Wrap(err, strings.Join(cmd.Args, " "))
	}
	return nil
}

func makePastaArgs(blocked, runcmd []string) ([]string, error) {
	script, err := pastaScript(blocked, runcmd)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	pastaArgs := []string{
		pastaBin, "--foreground", "--quiet",
		// Bring up the tap network interface.
		"--config-net",
		// No inbound port forwarding.
		"--tcp-ports", "none", "--udp-ports", "none",
		// Namespace's localhost and gateway address do not reach host.
		"--tcp-ns", "none", "--udp-ns", "none", "--no-map-gw",
		// Turn off unneeded stuff, similar to nsjail.
		"--no-dhcp", "--no-dhcpv6", "--no-ra", "--dns", "none",
		// Pin the names the setup script refers to.
		"--ns-ifname", nsIfname,
		// Addresses of the container.
		"--address", nsAddr4, "--gateway", nsGw4, "--netmask", "24",
		"--address", nsAddr6, "--gateway", nsGw6,
		"--", "/bin/sh", "-c", script}
	return pastaArgs, nil
}

func pastaScript(blocked, cmd []string) (string, error) {
	lines := []string{
		"set -eu",
		ipBin + " -4 route flush table main",
	}

	// Add default routes *through* gateway, but ban traffic *to* gateway.
	lines = append(lines, []string{
		fmt.Sprintf(ipBin+" -4 route add default via %s dev %s onlink", nsGw4, nsIfname),
		fmt.Sprintf(ipBin+" -4 route add prohibit %s/32", nsGw4),
		ipBin + " -6 route flush table main",
		fmt.Sprintf(ipBin+" -6 route add default via %s dev %s onlink", nsGw6, nsIfname),
		fmt.Sprintf(ipBin+" -6 route add prohibit %s/128", nsGw6),
	}...)

	// Block traffic in the blocked list.
	for _, b := range blocked {
		prefix, err := netip.ParsePrefix(b)
		if err != nil {
			return "", errors.Wrap(err, "")
		}
		if prefix.Addr().Is4() {
			lines = append(lines, fmt.Sprintf(ipBin+" -4 route add prohibit %s", prefix))
		} else {
			lines = append(lines, fmt.Sprintf(ipBin+" -6 route add prohibit %s", prefix))
		}
	}

	// Forward traffic from vethIn to vethOut.
	lines = append(lines, []string{
		"echo 1 > /proc/sys/net/ipv4/ip_forward",
		"echo 1 > /proc/sys/net/ipv6/conf/all/forwarding",
		fmt.Sprintf(ipBin+" link add %s type veth peer name %s", vethOut, vethIn),
		fmt.Sprintf(ipBin+" -4 addr add %s/30 dev %s", vethOutAddr4, vethOut),
		fmt.Sprintf(ipBin+" -6 addr add %s/64 dev %s nodad", vethOutAddr6, vethOut),
		fmt.Sprintf(ipBin+" link set %s up", vethOut),
	}...)
	// Prevent address spoofing by ensuring source is vethInAddr.
	lines = append(lines, []string{
		fmt.Sprintf(ipBin+" -4 rule add pref 100 iif %s from %s/32 lookup main", vethOut, vethInAddr4),
		fmt.Sprintf(ipBin+" -4 rule add pref 101 iif %s prohibit", vethOut),
		fmt.Sprintf(ipBin+" -6 rule add pref 100 iif %s from %s/128 lookup main", vethOut, vethInAddr6),
		fmt.Sprintf(ipBin+" -6 rule add pref 101 iif %s prohibit", vethOut),
	}...)

	// Run the inner script in a new network namespace, with pasta's
	// namespace open as file descriptor pastaNsFd.
	lines = append(lines,
		fmt.Sprintf("exec %d</proc/self/ns/net", pastaNsFd),
		"exec "+unshareBin+" --net -- /bin/sh -c "+shellQuote(innerScript(cmd)))

	return strings.Join(lines, "\n") + "\n", nil
}

// innerScript returns a script that runs in runsc's network namespace, with
// pasta's namespace open as file descriptor pastaNsFd. It moves the inner end
// of the veth pair into its own namespace, which it opens as file descriptor
// innerNsFd, configures the veth, and then executes cmd.
func innerScript(cmd []string) string {
	lines := []string{
		"set -eu",
		fmt.Sprintf("exec %d</proc/self/ns/net", innerNsFd),
		// nsenter enters pasta's namespace, and ip receives this
		// namespace as an inherited file.
		fmt.Sprintf("%s --net=/proc/self/fd/%d %s link set %s netns /proc/self/fd/%d", nsenterBin, pastaNsFd, ipBin, vethIn, innerNsFd),
		// Close the file descriptors pastaNsFd and innerNsFd.
		// This ensures runsc never touches pasta's network namespace.
		fmt.Sprintf("exec %d<&- %d<&-", pastaNsFd, innerNsFd),
	}

	// Set up the inner network namespace.
	lines = append(lines, []string{
		ipBin + " link set lo up",
		fmt.Sprintf(ipBin+" -4 addr add %s/30 dev %s", vethInAddr4, vethIn),
		fmt.Sprintf(ipBin+" -6 addr add %s/64 dev %s nodad", vethInAddr6, vethIn),
		fmt.Sprintf(ipBin+" link set %s up", vethIn),
		fmt.Sprintf(ipBin+" -4 route add default via %s", vethOutAddr4),
		fmt.Sprintf(ipBin+" -6 route add default via %s", vethOutAddr6),
	}...)

	// Run cmd in the inner network namespace.
	quoted := make([]string, len(cmd))
	for i, arg := range cmd {
		quoted[i] = shellQuote(arg)
	}
	lines = append(lines, "exec "+strings.Join(quoted, " "))

	return strings.Join(lines, "\n") + "\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func makeRunscArgs(id, dir string, cfg config) ([]string, error) {
	// The OCI bundle, with config.json and the root filesystem.
	bundle := filepath.Join(dir, "bundle")
	// runsc's container state, which is not part of the bundle.
	state := filepath.Join(dir, "state")

	if err := makeBundle(bundle, cfg); err != nil {
		return nil, errors.Wrap(err, "")
	}
	runscArgs := []string{runscBin,
		"--network=sandbox",
		// The systemd scope sets the resource limits.
		"--ignore-cgroups",
		"--root", state,
		// The root filesystem is writable, but writes stay in sandbox memory, which the scope's MemoryMax limits.
		"--overlay2", "root:memory",
		"run", "--bundle", bundle, id}
	return runscArgs, nil
}

func makeBundle(bundle string, cfg config) error {
	rootfs := filepath.Join(bundle, "rootfs")
	binds, err := makeRootfs(rootfs, cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	if err := writeSpec(bundle, newSpec(cfg, binds)); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func newSpec(cfg config, binds []specs.Mount) *specs.Spec {
	// runsc is being run in pasta's user namespace,
	// thus the effective UID/GID mapping the sentry's uid_map is 0.
	// For more info, see the section "Configuring the OCI Spec" in
	// https://gvisor.dev/docs/user_guide/rootless/
	const effectiveHostID = 0

	nofile := uint64(cfg.nofile)
	return &specs.Spec{
		Version: specs.Version,
		Process: &specs.Process{
			Args:            []string{cfg.execBin},
			Env:             cfg.env,
			Cwd:             "/",
			Rlimits:         []specs.POSIXRlimit{{Type: "RLIMIT_NOFILE", Hard: nofile, Soft: nofile}},
			NoNewPrivileges: true,
		},
		Root:     &specs.Root{Path: "rootfs"},
		Hostname: "sandbox",
		Mounts:   binds,
		Linux: &specs.Linux{
			Namespaces: []specs.LinuxNamespace{
				{Type: specs.UserNamespace},
				// runsc's own network namespace, which has the inner end of the veth pair.
				{Type: specs.NetworkNamespace, Path: "/proc/self/ns/net"},
			},
			UIDMappings: []specs.LinuxIDMapping{{ContainerID: 0, HostID: effectiveHostID, Size: 1}},
			GIDMappings: []specs.LinuxIDMapping{{ContainerID: 0, HostID: effectiveHostID, Size: 1}},
		},
	}
}

func writeSpec(bundle string, s *specs.Spec) error {
	b, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		return errors.Wrap(err, "")
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), b, 0644); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func makeRootfs(rootfs string, cfg config) ([]specs.Mount, error) {
	if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0755); err != nil {
		return nil, errors.Wrap(err, "")
	}
	if err := os.WriteFile(filepath.Join(rootfs, "etc", "resolv.conf"), []byte(cfg.resolvConf), 0644); err != nil {
		return nil, errors.Wrap(err, "")
	}

	binds := make([]specs.Mount, 0)
	seen := make(map[string]struct{})
	for _, p := range cfg.binds {
		p = filepath.Clean(p)
		realP, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, errors.Wrap(err, p)
		}

		// Reproduce symlink in rootfs if needed.
		if realP != p {
			if err := makeSymlink(rootfs, p, realP); err != nil {
				return nil, errors.Wrap(err, p)
			}
		}

		// Add mount point.
		if _, ok := seen[realP]; ok {
			continue
		}
		seen[realP] = struct{}{}
		if err := makeMountPoint(rootfs, realP); err != nil {
			return nil, errors.Wrap(err, p)
		}
		binds = append(binds, specs.Mount{Destination: realP, Type: "bind", Source: realP, Options: []string{"ro", "bind"}})
	}
	return binds, nil
}

func makeSymlink(rootfs, p, realP string) error {
	target, err := filepath.Rel(filepath.Dir(p), realP)
	if err != nil {
		return errors.Wrap(err, "")
	}
	dst := filepath.Join(rootfs, p)
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return errors.Wrap(err, "")
	}
	if err := os.Symlink(target, dst); err != nil {
		return errors.Wrap(err, "")
	}
	return nil
}

func makeMountPoint(rootfs, realP string) error {
	fi, err := os.Stat(realP)
	if err != nil {
		return errors.Wrap(err, "")
	}
	dst := filepath.Join(rootfs, realP)
	if fi.IsDir() {
		if err := os.MkdirAll(dst, 0755); err != nil {
			return errors.Wrap(err, "")
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return errors.Wrap(err, "")
		}
		if err := os.WriteFile(dst, nil, 0644); err != nil {
			return errors.Wrap(err, "")
		}
	}
	return nil
}

func stopUnit(unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", unit).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not loaded") {
		return errors.Wrap(err, string(out))
	}
	return nil
}

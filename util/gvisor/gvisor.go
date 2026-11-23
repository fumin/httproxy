// Package gvisor runs a gVisor sentry.
//
// Security layers starting from the outermost:
//   - Pasta, reads layer-2 ethernet network packets from the network interface
//     `vout` and translates them into layer-4 TCP/UDP packets which are sent to
//     the external internet through the host's network interface `eth0`.
//     Without root privileges, layer-4 packets are the only thing accepted by
//     `eth0`. This explains why Pasta is needed in our rootless solution here.
//   - An outer network namespace, created by Pasta. This namespace contains
//     firewall rules that allow only packets to the external internet and blocks
//     everything to LAN, thus achieving network isolation.
//   - An inner network namespace, where both the network interface `vin` and
//     runsc's sentry reside. This inner namespace is separated from the outer
//     namespace where the aforementioned firewall resides. This ensures that the
//     firewall cannot be edited by a guest who escapes gVisor into the sentry.
//   - gVisor and its sentry started by runsc. gVisor uses its own network stack
//     which prevents guests from exploiting bugs in Linux's networking code.
package gvisor

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Binaries used.
	pastaBin   = "pasta"
	runscBin   = "runsc"
	ipBin      = "/usr/bin/ip"
	unshareBin = "/usr/bin/unshare"
	nsenterBin = "/usr/bin/nsenter"
	ncBin      = "/usr/bin/nc"
	sleepBin   = "/usr/bin/sleep"
	setprivBin = "/usr/bin/setpriv"
	mountBin   = "/usr/bin/mount"
	shBin      = "/bin/sh"
	envBin     = "/usr/bin/env"
)

// Config configures a sandbox.
type Config struct {
	// Root is a host directory used as the sandbox's root filesystem.
	Root string
	// Mounts are mounted on top of Root.
	Mounts []specs.Mount
	// Blocked are destination ranges the sandbox cannot connect to.
	Blocked []string

	// CPUQuota is the percentage of one CPU.
	CPUQuota int
	// MemoryMax is the maximum memory in bytes.
	MemoryMax uint64
	// TasksMax is the maximum number of threads.
	TasksMax int

	// User is the user of processes run by Sandbox.Exec.
	User specs.User
	// Cwd is the working directory of processes run by Sandbox.Exec.
	Cwd string
	// Env is the environment of processes run by Sandbox.Exec.
	Env []string
}

// DefaultConfig returns a default Config.
func DefaultConfig(root string) Config {
	entries, err := os.ReadDir(filepath.Join(root, "home"))
	if err != nil {
		panic(fmt.Sprintf("%+v", err))
	}
	username := entries[0].Name()

	return Config{
		Root: root,
		Mounts: []specs.Mount{
			{Destination: "/home/" + username, Type: "tmpfs", Source: "tmpfs", Options: []string{"mode=0750", "uid=1000", "gid=1000"}},
			{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"mode=1777"}},
		},
		// Local area networks.
		Blocked: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "fc00::/7", "fe80::/10"},

		MemoryMax: 64 << 20,
		CPUQuota:  200,
		TasksMax:  256,

		User: specs.User{UID: 1000, GID: 1000},
		Cwd:  "/home/" + username,
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"TERM=xterm-256color",
			"VIMINIT=set nocompatible ruler showmode laststatus=2",
		},
	}
}

// run runs the sandbox until its main process exits or ctx is done.
// started is called with the container ID, runsc's state directory, and the
// host PID of the `runsc run` process, once the container has started.
func run(ctx context.Context, cfg Config, started func(id, state string, runscPid int)) error {
	id := "gvisor-" + util.RandStr()
	dir, err := os.MkdirTemp("", id)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer os.RemoveAll(dir)
	if err := addNeededMounts(&cfg, dir); err != nil {
		return errors.Wrap(err, "")
	}

	// Create the listener that will be notified when runsc has started.
	poststart, err := newPoststartListener(dir)
	if err != nil {
		return errors.Wrap(err, "")
	}
	defer poststart.Close()

	// Create command arguments.
	runscArgs, err := makeRunscArgs(id, dir, poststart.Socket(), cfg)
	if err != nil {
		return errors.Wrap(err, "")
	}
	pastaArgs, err := makePastaArgs(cfg.Blocked, runscArgs)
	if err != nil {
		return errors.Wrap(err, "")
	}
	rootMountArgs := makeRootMountArgs(cfg.Root, bundleRoot(dir), pastaArgs)

	// Create cmd so that the entire process tree is killed once we exit.
	// This is done by setting Pdeathsig which kills our direct child.
	// makePidnsArgs then makes sure all descendants are killed by making
	// that child PID 1.
	unit := id + ".scope"
	args := append(makeSystemdArgs(unit, cfg), makePidnsArgs()...)
	args = append(args, rootMountArgs...)
	runCtx, cancelCmd := context.WithCancel(ctx)
	defer cancelCmd()
	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	stderrBuf := util.NewTailBuffer(4096)
	cmd.Stderr = stderrBuf
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, strings.Join(cmd.Args, " "))
	}

	// Wait on cmd in the background.
	poststartCtx, cancelPoststart := context.WithCancel(ctx)
	defer cancelPoststart()
	cmdErrChan := make(chan error, 1)
	go func() {
		cmdErrChan <- cmd.Wait()
		cancelPoststart()
	}()

	// Wait for the poststart hook.
	poststartOk := func(runscPid int) {
		started(id, filepath.Join(dir, "state"), runscPid)
	}
	poststartErr := poststart.Listen(poststartCtx, poststartOk)
	if poststartErr != nil {
		cancelCmd() // cmd no longer needs to run if poststart fails
	}

	// Collect cmd's exit information.
	runErr := <-cmdErrChan
	// Clean up systemd.
	systemdRes, err := systemdCleanup(unit)
	if err != nil {
		return errors.Wrap(err, "")
	}

	// Report errors.
	//
	// runsc was asked to stop, and has stopped.
	if ctx.Err() != nil {
		return nil
	}
	// runsc got OOM killed.
	if systemdRes == "oom-kill" {
		return errors.Errorf("sandbox killed for exceeding MemoryMax=%d", cfg.MemoryMax)
	}
	// runsc's poststart hook failed.
	if poststartErr != nil {
		return errors.Wrap(poststartErr, "")
	}
	// runsc itself failed.
	if runErr != nil {
		return errors.Wrapf(runErr, "%s\nstderr: %s", strings.Join(cmd.Args, " "), stderrBuf)
	}
	return nil
}

func makeSystemdArgs(unit string, cfg Config) []string {
	return []string{
		"systemd-run", "--user", "--scope", "--quiet",
		"--unit=" + unit,
		"-p", fmt.Sprintf("MemoryMax=%d", cfg.MemoryMax),
		"-p", "MemorySwapMax=0",
		"-p", fmt.Sprintf("CPUQuota=%d%%", cfg.CPUQuota),
		"-p", fmt.Sprintf("TasksMax=%d", cfg.TasksMax),
	}
}

// systemdCleanup returns the result of unit, such as "success" or "oom-kill".
func systemdCleanup(unit string) (string, error) {
	out, err := exec.Command("systemctl", "--user", "show", "--property=Result", "--value", unit).Output()
	if err != nil {
		return "", errors.Wrap(err, "")
	}
	result := strings.TrimSpace(string(out))

	if result != "success" {
		// Clean up data held by systemd about the failed unit.
		if out, err := exec.Command("systemctl", "--user", "reset-failed", unit).CombinedOutput(); err != nil {
			return "", errors.Wrap(err, string(out))
		}
	}
	return result, nil
}

// makeRootMountArgs returns a command that mounts root read-only at rootMount,
// in a new mount namespace, then runs cmd without capabilities.
// This ensures root is always read-only.
func makeRootMountArgs(root, rootMount string, cmd []string) []string {
	quoted := make([]string, len(cmd))
	for i, arg := range cmd {
		quoted[i] = shellQuote(arg)
	}
	script := strings.Join([]string{
		"set -eu",
		mountBin + " --bind -o ro " + shellQuote(root) + " " + shellQuote(rootMount),
		// Drop the capabilities kept for mount, so that cmd runs without any.
		"exec " + setprivBin + " --inh-caps=-all --ambient-caps=-all -- " + strings.Join(quoted, " "),
	}, "\n")
	return []string{
		unshareBin,
		// Keep the capabilities granted in the new user namespace across exec, as mount requires them.
		"--user", "--map-current-user", "--keep-caps",
		"--mount",
		"--",
		shBin, "-c", script}
}

// makePidnsArgs returns a command that runs whatever follows as PID 1.
// This is achieved by creating a new PID namespace.
// When a PID 1 process of a namespace dies, the kernel kills all process of that namespace.
func makePidnsArgs() []string {
	return []string{
		unshareBin,
		// Creating a PID namespace without root requires a user namespace.
		"--user", "--map-current-user",
		// Create a PID namespace.
		"--pid",
		// Ensure child process is killed when unshare dies.
		"--kill-child",
		// Mount /proc as some processes need this, such as https://passt.top/
		"--mount-proc",
		"--"}
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
	// namespace opened as file descriptor pastaNsFd.
	lines = append(lines,
		fmt.Sprintf("exec %d</proc/self/ns/net", pastaNsFd),
		"exec "+unshareBin+" --net -- /bin/sh -c "+shellQuote(innerScript(cmd)))

	return strings.Join(lines, "\n") + "\n", nil
}

// innerScript returns a script that sets up the inner network namespace, then runs cmd.
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

func makeRunscArgs(id, dir, poststartSock string, cfg Config) ([]string, error) {
	// The OCI bundle, with config.json and the root filesystem.
	// https://github.com/opencontainers/runtime-spec/blob/main/runtime.md
	bundle := filepath.Join(dir, "bundle")
	// runsc's container state, which is not part of the bundle.
	state := filepath.Join(dir, "state")

	if err := writeSpec(bundle, newSpec(cfg, poststartSock)); err != nil {
		return nil, errors.Wrap(err, "")
	}

	runscArgs := []string{runscBin,
		"--root", state,
		// Writes to the root filesystem and bind mounts go to memory, never to the host.
		"--overlay2", "all:memory",
		// Honor SUID/SGID bits, so that su can switch to root.
		"--allow-suid",
		"--network=sandbox",
		// Without root, runsc cannot create cgroups.
		"--ignore-cgroups",
		"run", "--bundle", bundle, id}
	return runscArgs, nil
}

func newSpec(cfg Config, poststartSock string) *specs.Spec {
	// notifySock forwards info provided by runsc to poststartSock.
	notifySock := specs.Hook{
		Path:    ncBin,
		Args:    []string{ncBin, "-U", poststartSock},
		Timeout: new(10), // seconds
	}

	// runsc is being run in pasta's user namespace,
	// thus the effective UID/GID mapping the sentry's uid_map is 0.
	// For more info, see the section "Configuring the OCI Spec" in
	// https://gvisor.dev/docs/user_guide/rootless/
	const effectiveHostID = 0
	linux := &specs.Linux{
		Namespaces: []specs.LinuxNamespace{
			{Type: specs.UserNamespace},
			// runsc's own network namespace, which has the inner end of the veth pair.
			{Type: specs.NetworkNamespace, Path: "/proc/self/ns/net"},
		},
		UIDMappings: []specs.LinuxIDMapping{{ContainerID: 0, HostID: effectiveHostID, Size: 1}},
		GIDMappings: []specs.LinuxIDMapping{{ContainerID: 0, HostID: effectiveHostID, Size: 1}},
	}

	return &specs.Spec{
		Version: specs.Version,
		Process: &specs.Process{
			Args: []string{sleepBin, "infinity"},
			User: cfg.User,
			Env:  cfg.Env,
			Cwd:  cfg.Cwd,
			// The capabilities su needs, to set the IDs and groups of root.
			// Only the bounding set, so that processes have none until su runs.
			Capabilities: &specs.LinuxCapabilities{Bounding: []string{"CAP_SETUID", "CAP_SETGID"}},
		},
		Root:     &specs.Root{Path: "rootfs"},
		Hostname: "sandbox",
		Mounts:   cfg.Mounts,
		Hooks: &specs.Hooks{
			Poststart: []specs.Hook{notifySock},
		},
		Linux: linux,
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

func addNeededMounts(cfg *Config, dir string) error {
	if err := os.MkdirAll(bundleRoot(dir), 0755); err != nil {
		return errors.Wrap(err, "")
	}
	mounts := filepath.Join(dir, "mounts")
	if err := os.MkdirAll(mounts, 0755); err != nil {
		return errors.Wrap(err, "")
	}

	resolv := filepath.Join(mounts, "resolv.conf")
	if err := os.WriteFile(resolv, []byte("nameserver 1.1.1.1\nnameserver 8.8.8.8\n"), 0644); err != nil {
		return errors.Wrap(err, "")
	}
	cfg.Mounts = append(cfg.Mounts, specs.Mount{Destination: "/etc/resolv.conf", Type: "bind", Source: resolv, Options: []string{"bind"}})

	return nil
}

func bundleRoot(dir string) string {
	return filepath.Join(dir, "bundle", "rootfs")
}

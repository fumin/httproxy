// Package dockerseccomp provides Docker's default seccomp profile as a Kafel policy for nsjail.
package dockerseccomp

import _ "embed"

// Policy is Docker's default seccomp profile as a Kafel policy, see seccomp_gen.go.
//
// To update it, replace docker_seccomp.json with the latest https://github.com/moby/profiles/blob/main/seccomp/default.json,
// update the commit in the -source flag below, and run go generate.
// The -kafel flag must point to the Kafel sources that the nsjail binary was built with.
//
//go:generate go run seccomp_gen.go -profile docker_seccomp.json -source https://github.com/moby/profiles/blob/9ee3204f0d0cfa19ca2c6a7b0fcf5a2b353954ae/seccomp/default.json -kafel $HOME/nsjail/kafel/src/syscalls/amd64_syscalls.c -unistd /usr/include/x86_64-linux-gnu/asm/unistd_64.h -o seccomp.kafel
//go:embed seccomp.kafel
var Policy string

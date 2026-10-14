# Docker isolation profile

`seccomp-bubblewrap.json` is Moby's default seccomp profile from
[`seccomp/v0.2.3`](https://github.com/moby/profiles/blob/836ae4d37ef2ec995c77c99fc55f5b5f3af3a897/seccomp/default.json),
with one final allow rule for `clone`, `unshare`, `mount`, `umount2` and
`pivot_root`. All other default restrictions remain. The upstream Apache 2.0
license is in `MOBY-LICENSE`. Review the final rule when updating the vendored
default profile.

The opt-in Compose overlay drops all outer capabilities, retains a non-root
user, and enables no-new-privileges. The verified host also requires explicitly
relaxing its AppArmor profile and protected system-path masks so Bubblewrap can
mount a fresh proc filesystem in its PID namespace. These outer protections
are changed only by this overlay; ordinary Compose keeps Docker defaults.
An equivalent loaded AppArmor policy can replace `apparmor:unconfined` if it
permits this namespace/mount contract on your host.

The supported profile uses neither privileged mode nor SYS_ADMIN. Missing
prerequisites fail explicitly; no caller retries without the sandbox. Kernel,
daemon and LSM policies differ, so run the container acceptance script on the
deployment host before enabling isolation.

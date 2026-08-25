# Release Plan

## Required artifacts

- `sss_<version>_linux_amd64`
- `sss_<version>_linux_arm64`
- `sss_<version>_windows_amd64.exe`
- Windows arm64 when cleanly supported
- `sssend` and `ssrecv` symlink instructions for Linux
- `sssend.cmd` and `ssrecv.cmd` or equivalent Windows shims
- `SHA256SUMS`
- systemd unit
- Caddy example
- config example
- install, upgrade, and recovery docs
- license and notice files required by dependencies
- release evidence report

## Version behavior

```bash
sss version
```

Must print:

- semantic version;
- commit;
- build date;
- protocol version;
- dirty flag when applicable.

## Reproducibility

Document one command that builds all supported targets from a clean checkout. Pin dependencies through `go.mod` and `go.sum`. Record toolchain version.

## Release candidate procedure

1. Freeze public contracts.
2. Build from clean checkout.
3. Run unit, integration, race, black-box, fault, and cross-platform suites.
4. Install server on a clean Debian 11 environment.
5. Install client on clean Linux and Windows environments.
6. Execute documented curl and CLI recipes.
7. Run upgrade test from previous release when applicable.
8. Generate checksums.
9. Independent manager audit.
10. Publish only after all gates pass.

## Evidence bundle

Release should include or link:

- test summary;
- raw CI logs;
- fault matrix;
- benchmark context;
- clean-install transcript;
- curl transcript;
- Windows transcript;
- known limitations.

A release is blocked when evidence is missing for a required gate.

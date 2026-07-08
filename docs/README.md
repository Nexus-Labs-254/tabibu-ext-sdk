# Tabibu Extension SDK — Documentation

| Document | What it covers |
|---|---|
| [overview.md](overview.md) | Why extensions exist, the architecture, stdio protocol, lifecycle, environment variables |
| [tutorial-mpesa-payments.md](tutorial-mpesa-payments.md) | End-to-end tutorial building a realistic M-Pesa payment extension — manifest, implementation, packaging, install, dev workflow |
| [manifest-reference.md](manifest-reference.md) | Every `manifest.toml` key, its type, default, and effect |
| [security.md](security.md) | Privilege model, authentication paths, PHI handling, API key and JWT lifecycle, dependency isolation |

Start with **overview.md**, then follow the **tutorial** to build your first extension.
Use **manifest-reference.md** and **security.md** as reference when you need them.

The `examples/hello-world/` directory in this repo is a minimal runnable extension —
no UI, no broker, just HTTP endpoints and config hot-reload. Use it as a build scaffold.

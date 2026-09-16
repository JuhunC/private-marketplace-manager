Initial release of Private Marketplace Manager and marketplace-sync.

- Linux manager: password-protected web inventory, manual uploads, authenticated REST API, embedded guide, durable SQLite inventory, idempotent VSIX publication, and startup recovery.
- Native sync client: Windows/Linux/macOS on x64 and ARM64; editable extension list; all discoverable versions, channels, and published platform variants; safe reruns and migration using server inventory.
- Manager image: `ghcr.io/juhunc/private-marketplace-manager:0.1.0` for Linux x64/ARM64.
- Download the client archive matching your OS/CPU and verify `SHA256SUMS`. No Go/Python/PowerShell installation required.

See the repository README and deployment guide before use. The initial release supports one operator password and one automation token. It validates package structure and content identity but does not perform publisher-signature verification or malware scanning. Marketplace version visibility depends on your existing Microsoft container. Native CI tests Linux x64, Windows x64, and macOS ARM64; other CPU builds require validation on your hardware. macOS binaries are not Apple-notarized.

Public gallery records can omit removed/unpublished versions. The tool reports source/transfer failures; it cannot reconstruct unavailable packages. The receiver requires operator deployment with write permission on the existing VSIX directory.

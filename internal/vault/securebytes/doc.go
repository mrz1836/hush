// Package securebytes provides the SecureBytes container — an opaque,
// pointer-only secret holder that pins its payload in non-swappable
// memory, zeroes the payload on explicit Destroy AND on garbage
// collection (via a runtime finalizer), and renders as the literal
// string "[redacted]" through every standard log/format/JSON path.
//
// SecureBytes is the foundation of hush's Layer 5 (mlocked secure
// memory) defense; see docs/SECURITY.md §3.5 and §6.
//
// # Residual risk
//
// mlock pins the current backing region against swap and against
// relocation of the pinned region, but the Go runtime may transiently
// copy heap objects during GC compaction in pathological cases. That
// transient copy may land in unprotected (unlocked) memory. This is
// documented as outside the package's threat model (commodity malware
// enumerating dotfiles, NOT root-level memory forensics) and no
// bandaid mitigation is added beyond the pinned-region design.
package securebytes

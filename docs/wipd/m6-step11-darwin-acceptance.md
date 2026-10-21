# M6 Step 11 Darwin checkout-discovery limitation

For the isolated Step 11 implementation, checkout discovery on Darwin is
explicitly unavailable and must fail closed before any authority request. The
implementation must not use PID-only peer identity, private `libproc`/SPI,
caller-supplied paths, caller-supplied Clone/Repo/remote assertions, or an
unapproved fallback. Preserve the existing Darwin daemon behavior.

This is not a waiver for cross-platform M6 parity or production cutover. Darwin
daemon and checkout-discovery support remain outstanding until a separately
authorized trusted platform mechanism is implemented and runtime-validated.
Cross-compilation/build evidence is not runtime validation; when Darwin runtime
tests are unavailable, record that explicitly.

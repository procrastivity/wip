# Draft MODEL §11 amendment — first tracker provider capability

Status: draft for the Phase 3 close. Do not apply before ratification.

Add the following refusal case to MODEL §11:

> Refuse automatic tracker state delivery when the configured provider cannot
> enforce the proposed transition with an atomic provider-side lease or
> equivalent conditional write. Keep the outbox entry visible as withheld
> with the provider's reason. Creation and narrated comments can remain
> supported independently.

Rationale: the first concrete provider, GitHub Issues, supports idempotent item
creation and Stage comments. Its public Issues REST surface does not provide
the atomic state-transition guard required by T1 and T6. The adapter therefore
refuses `state` before HTTP. Tracker traces 1, 3, 4, 5, 6, and 7 record this
capability refusal. Trace 8 proves that refusal preserves never-backward
rather than weakening it.

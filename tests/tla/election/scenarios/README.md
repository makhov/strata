# Election Scenarios

These JSON files are small protocol traces replayed against the real
`internal/election.Lock` implementation.

They are the Go-side bridge for the TLA+ election model:

- TLA+ explores abstract interleavings and invariants.
- These scenarios pin important traces to the implementation.
- Future TLC counterexamples can be translated into this format and replayed as
  regression tests.

Supported actions:

- `try_acquire`
- `takeover`
- `read`

Each step may assert the operation result with `expect_won`, the returned record
with `expect_record`, and the object-store lock state with `expect_lock`.

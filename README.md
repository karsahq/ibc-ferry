# ibc-ferry

Go implementation (planned).

The working Python prototype lives on the `prototype/python` branch. This
branch is reserved for the Go port.

## Target

- Start with the same one-way receive behaviour as the prototype.
- No database or daemon by default.
- Keep the client creation/proof-height strategy unless measurements show a
  better approach.
- Add acknowledgements, timeouts, and client updates incrementally.

See `prototype/python` for the validated flow and the Karsa/Gaia integration
harnesses.

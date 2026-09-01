# Security policy

## Supported versions

The latest release. This is a small tool with no dependencies; fixes ship in a
new tag rather than as backports.

## Reporting a vulnerability

Use GitHub's private reporting: **Security → Report a vulnerability** on
<https://github.com/steamvogue/djgyrofix/security/advisories/new>. Please do not
open a public issue for anything exploitable.

Expect an acknowledgement within a week.

## What counts here

djgyrofix parses untrusted binary input and then writes to the file it parsed,
so the interesting failures are:

- **A malformed MP4 or protobuf sample that panics, hangs, or allocates without
  bound.** Both parsers walk length fields taken straight from the file. They
  are fuzzed in CI, but a reproducer that gets past that is a real finding.
- **A write landing outside a `djmd` sample payload.** The tool asserts that
  every write is exactly four bytes at an offset the protobuf scanner found, and
  that the file size never changes. A crafted file that breaks either invariant
  is a serious bug — it means the tool can corrupt video data, not just
  metadata.
- **A path in the journal escaping its directory**, or a journal causing writes
  the user did not ask for.

## What does not count

- **A file that makes djgyrofix exit with an error.** Rejecting malformed input
  is the intended behaviour, not a denial of service.
- **Detection flagging the wrong thing.** That is a correctness bug — please
  open a normal issue with the output of `djgyrofix info --all-variants FILE`.
- **Anything requiring the attacker to already be able to write to your
  footage.** At that point they do not need this tool.

## If a patch damaged a file

It is almost certainly recoverable, and this is worth trying before reporting
anything. Every applied patch writes a sidecar journal holding the original
bytes of every range it touched:

```bash
djgyrofix verify DJI_0042.MP4          # what does not match
djgyrofix revert --force DJI_0042.MP4  # put the original bytes back
```

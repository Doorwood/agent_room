# Security

This is an early-stage tool for trusted collaborators, not a sandbox or
multi-tenant security boundary. Approved participants share the execution
owner's Codex session and can request work using that owner's filesystem,
credentials, and network authority. Approve only people you trust with that
access. Transcript attribution is not tamper-proof against the owner.

Keep host state and client credentials private and outside the project.
Share the complete session ID over a trusted channel; it pins the host's TLS
certificate. Admission still requires host approval. Restrict network access
to intended participants. Revoking access does not undo or cancel previously
accepted work.

Do not post vulnerabilities, credentials, transcripts, or real deployment
details in public issues. Use GitHub's private vulnerability reporting on
this repository. If that option is unavailable, open an issue asking for a
private reporting channel without including vulnerability details.

There is no guaranteed security response time or supported stable release yet.

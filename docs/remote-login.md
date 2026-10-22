# Graphical login from a remote terminal

Corresync can show its remote Outlook Web login browser in a normal local
browser. Install the same Corresync version on both computers for the helper.
The account and Chromium must be configured on the remote Linux host. The
session stays there after sign-in. This does not authenticate a separate local
Corresync account. Teams Web uses a separate browser and is not yet supported by
this relay.

## Start on your Mac or local computer

```console
corr auth login --host nvidia7 --account work
```

Use an SSH alias reachable **from your Mac**. The helper uses your existing SSH
configuration and host-key verification. Put a port or `ProxyJump` in
`~/.ssh/config` when the host requires a jump server. No Orca installation is
required. The helper opens your browser on macOS/Linux; if opening fails, copy
the printed URL into your local browser. Windows users can open the printed URL
manually. The helper transfers bounded screen frames and human input over SSH;
it opens no remote network service.

## Start inside nvidia7 or an Orca remote terminal

Run **on the remote host**:

```console
corr auth login --account work --web
```

Keep that command running. It prints a port and a complete private login link.
Then run its `ssh -N -L ... SSH_HOST` command **in a terminal on your Mac**, replacing
`SSH_HOST` with a host alias reachable from the Mac. Open the printed login link
in the Mac browser. A remote `127.0.0.1` address is not your Mac's localhost until
this tunnel exists. If either port is occupied, restart the remote command with
`--web-port PORT` using a port free on both computers. Keep the forwarding process
running until completion, then stop it with Ctrl-C.

Orca may offer port forwarding, but that integration is unverified. Follow the
plain SSH instructions even when Orca hosts the original terminal. Do not expose
the UI with a public tunnel, bind it to a network interface, or share the link.

## Operate and finish

Click the screen and type normally. Keyboard navigation, scrolling, IME input,
and explicit paste are supported. The fixed remote viewport scales with your
window. Input is bounded to 4 KiB per insertion. The header shows the remote
HTTPS origin without identity-provider query parameters. Verify that origin
before entering authentication factors. The page does not store input or frames.

The completion message means you can close the tab; the remote daemon keeps the
account session. Cancel or Ctrl-C closes an unfinished login. Closing the tab
alone allows same-tab reload within the ten-minute UI lifetime (the configured
browser timeout can be shorter). Another controller cannot take over. A failed
interaction is not automatically retried: refresh to inspect the result before
repeating any action. Restart the command if the SSH connection or login expired.

Local Touch ID/platform passkeys, USB security keys, and client certificates
are not forwarded. Use a supported provider login environment if policy requires
those devices. No flow bypasses MFA, Conditional Access, or a permission check.
The existing visible-browser login and `--terminal` remain available.

Synthetic tests cover the bounded transport and browser input. A live login from
a local Mac to Linux, including the Orca workflow, remains unobserved; the
maintainer must perform it explicitly. Never put real credentials into fixtures,
logs, issue reports, screenshots, or agent prompts.
